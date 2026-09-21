#!/bin/sh
# Render the config templates, then run Asterisk in the foreground.
#
# The templates are rendered rather than mounted so the SIP password stays in
# the environment and never lands in a git-tracked file. envsubst is given an
# explicit variable list: without it, it would also eat Asterisk's own
# ${EXTEN} and ${CALLERID(num)}, which are not shell variables at all.
set -eu

: "${ASTERISK_SIP_PASSWORD:?set ASTERISK_SIP_PASSWORD (see .env.example)}"
: "${ASTERISK_SIP_USER:=1001}"
: "${VOCAT_TRUNK_HOST:=127.0.0.1:5062}"
: "${VOCAT_DEVICE:=}"
: "${ASTERISK_AMI_USER:=vocat}"
: "${ASTERISK_AMI_SECRET:=}"

if [ ${#ASTERISK_SIP_PASSWORD} -lt 12 ]; then
	# SIP registrars are scanned constantly and a weak one becomes someone
	# else's long-distance plan. This PBX has a real SIM behind it.
	echo "entrypoint: ASTERISK_SIP_PASSWORD must be at least 12 characters" >&2
	exit 1
fi

# A comma is the argument separator inside an extensions.conf application
# call, so "Set(__VOCATDEV=a,b,c)" parses as Set with three stray arguments,
# the _. extension fails to load, and every call is answered 404 by Asterisk
# before it ever reaches VoCat. VoCat accepts commas or spaces between device
# names, so normalising to spaces here keeps .env readable and the dialplan
# valid.
VOCAT_DEVICE=$(printf '%s' "$VOCAT_DEVICE" | tr ',' ' ')

# AMI is off unless a secret is set, so a deployment that does not use the
# Asterisk status page never exposes a manager account at all.
ASTERISK_AMI_ENABLED=no
if [ -n "$ASTERISK_AMI_SECRET" ]; then
	if [ ${#ASTERISK_AMI_SECRET} -lt 12 ]; then
		echo "entrypoint: ASTERISK_AMI_SECRET must be at least 12 characters" >&2
		exit 1
	fi
	ASTERISK_AMI_ENABLED=yes
fi

export ASTERISK_SIP_PASSWORD ASTERISK_SIP_USER VOCAT_TRUNK_HOST VOCAT_DEVICE
export ASTERISK_AMI_USER ASTERISK_AMI_SECRET ASTERISK_AMI_ENABLED
substitute='$ASTERISK_SIP_PASSWORD $ASTERISK_SIP_USER $VOCAT_TRUNK_HOST $VOCAT_DEVICE'
substitute="$substitute"' $ASTERISK_AMI_USER $ASTERISK_AMI_SECRET $ASTERISK_AMI_ENABLED'

for template in /etc/asterisk/templates/*.conf; do
	[ -e "$template" ] || continue
	envsubst "$substitute" < "$template" > "/etc/asterisk/$(basename "$template")"
done

# Refuse to start on top of another SIP server. This container uses host
# networking, so port 5060 is the host's: an Asterisk installed on the host
# itself, or another PBX, silently wins the bind. Asterisk then starts
# perfectly happily with no SIP transport, `pjsip show endpoints` still lists
# everything, and a softphone's REGISTER is answered by the *other* server --
# which rejects an account it has never heard of. Every signal points at
# credentials, and this container's log stays empty because it never saw the
# packet. Failing loudly here costs one restart loop and saves that hunt.
occupied=""
for protocol in u t; do
	# $4 is the local address; $5 is the peer, and using it would mean this
	# check never fires. NR>1 drops the header row.
	if ss -ln"$protocol" 2>/dev/null | awk 'NR>1 {print $4}' | grep -qE '(^|:)5060$'; then
		occupied="$occupied $protocol"
	fi
done
if [ -n "$occupied" ]; then
	echo "entrypoint: port 5060 is already in use on this host ($occupied)." >&2
	echo "entrypoint: this container uses host networking, so it cannot bind it." >&2
	echo "entrypoint: something else -- often an Asterisk installed on the host --" >&2
	echo "entrypoint: will answer your softphone instead, and reject it." >&2
	echo "entrypoint: find it with:  sudo ss -lunp | grep :5060" >&2
	echo "entrypoint: if it is a host Asterisk:  sudo systemctl disable --now asterisk" >&2
	exit 1
fi

# With no secret the rendered manager.conf would still declare an account,
# inert but present. Replacing the file outright leaves nothing to reach.
if [ "$ASTERISK_AMI_ENABLED" != "yes" ]; then
	printf '[general]\nenabled = no\n' > /etc/asterisk/manager.conf
fi

# The dialplan's routes come from a file VoCat generates into the directory
# both containers share. extensions.conf includes it unconditionally, so it
# has to exist before Asterisk parses anything -- a missing include leaves
# [vocat-routes] undefined, from-internal matching nothing, and every call
# answered 404.
#
# Written only when absent: VoCat owns it afterwards, and overwriting on every
# restart would silently discard whatever was configured in the web UI.
mkdir -p /etc/asterisk/vocat
if [ ! -f /etc/asterisk/vocat/routes.conf ]; then
	echo "entrypoint: writing the default route (everything out through the SIM)"
	{
		echo "; Default written by entrypoint.sh because no routes were configured."
		echo "; VoCat replaces this file when routes are saved in the web UI."
		echo ""
		echo "[vocat-routes]"
		echo "exten => _.,1,Set(__VOCATDEV=$VOCAT_DEVICE)"
		echo " same => n,Dial(PJSIP/\${EXTEN}@vocat,60,b(vocat-predial^s^1))"
		echo " same => n,Hangup()"
	} > /etc/asterisk/vocat/routes.conf
fi

# Softphone accounts come from a file VoCat generates into the same shared
# directory. pjsip.conf includes it unconditionally, so it has to exist before
# Asterisk parses anything -- a missing include there costs every account in
# the file, not just one.
#
# Seeded from the environment only when absent, exactly like routes.conf:
# VoCat owns it once anything is saved in the web UI, and rewriting it on
# every restart would silently put the .env account back and drop the rest.
if [ ! -f /etc/asterisk/vocat/endpoints.conf ]; then
	echo "entrypoint: seeding extension $ASTERISK_SIP_USER from the environment"
	{
		echo "; Seeded by entrypoint.sh from ASTERISK_SIP_USER/ASTERISK_SIP_PASSWORD."
		echo "; VoCat replaces this file when extensions are saved in the web UI."
		echo ""
		echo "[$ASTERISK_SIP_USER]"
		echo "type=endpoint"
		echo "context=from-internal"
		echo "disallow=all"
		echo "allow=ulaw"
		echo "allow=alaw"
		echo "auth=$ASTERISK_SIP_USER"
		echo "aors=$ASTERISK_SIP_USER"
		echo "rtp_symmetric=yes"
		echo "force_rport=yes"
		echo "rewrite_contact=yes"
		echo "direct_media=no"
		echo ""
		echo "[$ASTERISK_SIP_USER]"
		echo "type=auth"
		echo "auth_type=userpass"
		echo "username=$ASTERISK_SIP_USER"
		echo "password=$ASTERISK_SIP_PASSWORD"
		echo ""
		echo "[$ASTERISK_SIP_USER]"
		echo "type=aor"
		echo "max_contacts=2"
		echo "remove_existing=yes"
		echo "qualify_frequency=60"
	} > /etc/asterisk/vocat/endpoints.conf
fi
# The file holds a SIP password in the clear, which is what Asterisk needs to
# answer a digest challenge. Narrow it whether this run wrote it or not: an
# older deployment seeded one before this line existed.
chmod 600 /etc/asterisk/vocat/endpoints.conf

# The matching dialplan half, so the seeded account can be dialled from
# another handset. extensions.conf includes it unconditionally; without the
# file, [vocat-internal] is undefined and from-internal includes a context
# that does not exist.
if [ ! -f /etc/asterisk/vocat/internal.conf ]; then
	{
		echo "; Seeded by entrypoint.sh alongside the extension above."
		echo "; VoCat replaces this file when extensions are saved in the web UI."
		echo ""
		echo "[vocat-internal]"
		echo "exten => $ASTERISK_SIP_USER,1,Dial(PJSIP/$ASTERISK_SIP_USER,30)"
		echo " same => n,Hangup()"
	} > /etc/asterisk/vocat/internal.conf
fi

# Where an inbound call rings, generated by VoCat from the inbound mode chosen
# in the web UI. Seeded to ring the .env extension so a fresh deployment
# behaves as it always did; extensions.conf includes it unconditionally.
if [ ! -f /etc/asterisk/vocat/inbound.conf ]; then
	{
		echo "; Seeded by entrypoint.sh: ring the extension from .env."
		echo "; VoCat replaces this file when the inbound mode is saved in the web UI."
		echo ""
		echo "[vocat-inbound]"
		echo "exten => _.,1,Dial(PJSIP/$ASTERISK_SIP_USER,30)"
		echo " same => n,Hangup()"
	} > /etc/asterisk/vocat/inbound.conf
fi

# External SIP trunks whose calls are relayed out through a SIM, generated by
# VoCat from the web UI. Seeded empty rather than with an example: a trunk is
# a peer that can spend money on a SIM, and one nobody asked for is not a
# sensible default. pjsip.conf and extensions.conf include these two
# unconditionally, so the files have to exist even with no trunks configured.
if [ ! -f /etc/asterisk/vocat/trunks.conf ]; then
	{
		echo "; Seeded empty by entrypoint.sh."
		echo "; VoCat replaces this file when inbound trunks are saved in the web UI."
		echo ""
		echo "; No inbound trunks configured."
	} > /etc/asterisk/vocat/trunks.conf
fi
# It holds SIP passwords once VoCat writes it. Narrow it whether this run
# created it or not, the same way endpoints.conf is narrowed above.
chmod 600 /etc/asterisk/vocat/trunks.conf

# SMS dialplan, generated by VoCat. Seeded with forwarding off: delivering
# texts to handsets is new behaviour and must not begin on its own when an
# existing deployment restarts into a newer image.
if [ ! -f /etc/asterisk/vocat/messages.conf ]; then
	{
		echo "; Seeded by entrypoint.sh with SMS forwarding off."
		echo "; VoCat replaces this file when SMS routing is saved in the web UI."
		echo ""
		echo "[vocat-messages]"
		echo "exten => _.,1,NoOp(SMS forwarding is off: \${EXTEN})"
		echo " same => n,Hangup()"
	} > /etc/asterisk/vocat/messages.conf
fi

if [ ! -f /etc/asterisk/vocat/trunk-routes.conf ]; then
	{
		echo "; Seeded empty by entrypoint.sh."
		echo "; VoCat replaces this file when inbound trunks are saved in the web UI."
		echo ""
		echo "; No inbound trunks configured."
	} > /etc/asterisk/vocat/trunk-routes.conf
fi

echo "entrypoint: trunk=$VOCAT_TRUNK_HOST device=${VOCAT_DEVICE:-<auto>} extension=$ASTERISK_SIP_USER"
echo "entrypoint: manager interface (AMI) enabled=$ASTERISK_AMI_ENABLED"
echo "entrypoint: PJSIP is the only SIP driver here; chan_sip is noloaded."
echo "entrypoint: if a softphone gets 'Wrong password', check that res_pjsip"
echo "entrypoint: loaded and bound 5060 -- run: asterisk -rx 'pjsip show endpoints'"

# -f keeps Asterisk in the foreground so the container supervises it and its
# console output becomes `docker compose logs`.
exec asterisk -f -U root -G root "$@"
