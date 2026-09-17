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

export ASTERISK_SIP_PASSWORD ASTERISK_SIP_USER VOCAT_TRUNK_HOST VOCAT_DEVICE
substitute='$ASTERISK_SIP_PASSWORD $ASTERISK_SIP_USER $VOCAT_TRUNK_HOST $VOCAT_DEVICE'

for template in /etc/asterisk/templates/*.conf; do
	[ -e "$template" ] || continue
	envsubst "$substitute" < "$template" > "/etc/asterisk/$(basename "$template")"
done

echo "entrypoint: trunk=$VOCAT_TRUNK_HOST device=${VOCAT_DEVICE:-<auto>} extension=$ASTERISK_SIP_USER"

# -f keeps Asterisk in the foreground so the container supervises it and its
# console output becomes `docker compose logs`.
exec asterisk -f -U root -G root "$@"
