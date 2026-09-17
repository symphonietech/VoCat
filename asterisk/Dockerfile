# Asterisk for the VoCat SIP trunk.
#
# Ubuntu 26.04 LTS, which carries Asterisk 22.5 -- the current upstream LTS.
#
# Not 24.04: its Asterisk 20 leaves full upstream support on 2026-10-19 and
# drops to security-only fixes for a year after that. Starting a new
# deployment one month before that line makes no sense when the next LTS is
# already out.
#
# Not Debian: Asterisk was dropped before bookworm released, so
# `apt-get install asterisk` there fails outright.
#
# A distribution package rather than an image from a registry, because a PBX
# with a SIM's call charges behind it is not somewhere to run something nobody
# is patching. Caveat worth knowing: asterisk is in universe, which is
# community-maintained for security rather than covered by Canonical's main
# support (Ubuntu Pro extends to universe if you have it).
#
# If this base ever needs rolling back, ubuntu:24.04 and Asterisk 20.6 worked
# with these same configs -- but see modules.conf, which matters again there.
FROM ubuntu:26.04

# universe is enabled in this image's default sources, so no extra repository
# setup is needed.
#
# --no-install-recommends skips music-on-hold and sox, which this trunk never
# plays: it passes the far end's audio through. asterisk-config and
# asterisk-modules arrive as hard dependencies. asterisk-core-sounds-en-gsm is
# listed explicitly to settle the virtual asterisk-core-sounds-en dependency,
# which several packages provide. gettext-base is for the entrypoint's
# envsubst, and iproute2 for the port check the entrypoint does before
# starting Asterisk.
#
# If a dial plan you add later reports a missing module, drop the flag and
# rebuild -- the recommends are what carry the optional extras.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      asterisk \
      asterisk-core-sounds-en-gsm \
      gettext-base \
      iproute2 \
 && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
