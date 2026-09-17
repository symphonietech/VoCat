# Asterisk for the VoCat SIP trunk.
#
# Ubuntu 24.04 rather than Debian: Asterisk was dropped from Debian before
# bookworm released, so `apt-get install asterisk` there fails outright.
# Noble carries 20.6.0 -- an upstream LTS release -- and is itself an LTS
# base. It is a distribution package rather than an image from a registry
# because a PBX with a SIM's call charges behind it is not somewhere to run
# something nobody is patching.
#
# Caveat worth knowing: asterisk is in universe, which is community-maintained
# for security rather than covered by Canonical's main support (Ubuntu Pro
# extends to universe if you have it).
FROM ubuntu:24.04

# universe is enabled in this image's default sources, so no extra repository
# setup is needed.
#
# --no-install-recommends skips music-on-hold and sox, which this trunk never
# plays: it passes the far end's audio through. asterisk-config and
# asterisk-modules arrive as hard dependencies. asterisk-core-sounds-en-gsm is
# listed explicitly to settle the virtual asterisk-core-sounds-en dependency,
# which several packages provide. gettext-base is for the entrypoint's
# envsubst.
#
# If a dial plan you add later reports a missing module, drop the flag and
# rebuild -- the recommends are what carry the optional extras.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      asterisk \
      asterisk-core-sounds-en-gsm \
      gettext-base \
 && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
