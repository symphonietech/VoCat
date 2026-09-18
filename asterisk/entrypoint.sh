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

export ASTERISK_SIP_PASSWORD ASTERISK_SIP_USER VOCAT_TRUNK_HOST VOCAT_DEVICE
substitute='$ASTERISK_SIP_PASSWORD $ASTERISK_SIP_USER $VOCAT_TRUNK_HOST $VOCAT_DEVICE'

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

echo "entrypoint: trunk=$VOCAT_TRUNK_HOST device=${VOCAT_DEVICE:-<auto>} extension=$ASTERISK_SIP_USER"
echo "entrypoint: PJSIP is the only SIP driver here; chan_sip is noloaded."
echo "entrypoint: if a softphone gets 'Wrong password', check that res_pjsip"
echo "entrypoint: loaded and bound 5060 -- run: asterisk -rx 'pjsip show endpoints'"

# -f keeps Asterisk in the foreground so the container supervises it and its
# console output becomes `docker compose logs`.
exec asterisk -f -U root -G root "$@"
