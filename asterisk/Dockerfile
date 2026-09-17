# Asterisk for the VoCat SIP trunk.
#
# Built from Debian's package rather than a third-party image: bookworm ships
# Asterisk 20, which is an LTS release, and the package is the one that gets
# Debian's security updates. A PBX with a SIM's call charges behind it is not
# somewhere to trust an unmaintained image from a registry.
FROM debian:bookworm-slim

# --no-install-recommends keeps this to the modules the trunk actually uses.
# asterisk-config supplies the /etc/asterisk defaults the entrypoint renders
# on top of; the sounds package is what Asterisk plays for anything that is
# not a straight pass-through of the far end's ringback. If a dial plan you
# add later reports a missing module, drop the flag and rebuild.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      asterisk \
      asterisk-config \
      asterisk-core-sounds-en-gsm \
      gettext-base \
 && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
