# Carrier profile overrides

VoCat reads carrier profile overrides from `carrier-profiles.d/` inside its
data directory — `/opt/vocat/data/carrier-profiles.d` in the container, which
`docker-compose.yml` bind-mounts to `./data/carrier-profiles.d` on the host
(or `$VOCAT_DATA_DIR/carrier-profiles.d`). Create the directory if it is not
there yet; a missing one simply means no overrides.

Files are loaded **once at startup**, after the profile database compiled into
the binary. Restart the container after editing:

```sh
docker compose restart vocat
```

Only `*.json` files directly in that directory are read — no
subdirectories, no symlinks. Each file is capped at 1 MiB and the directory
at 256 entries.

## File format

Same schema as the built-in database — a version and a list of rules:

```json
{
  "version": 1,
  "profiles": [
    {
      "id": "local-globe-51502",
      "match": {
        "home_plmns": ["51502"],
        "iccid_prefixes": ["896342"]
      },
      "epdg": {
        "hostname": "weconnect.globe.com.ph"
      }
    }
  ]
}
```

Selectors available under `match` (and each entry of `match_any`, which is an
OR-list of alternative selectors): `home_plmns`, `imsi_prefixes`,
`iccid_prefixes`, `spns`, `gid1_prefixes`, `gid2_prefixes`.

Beyond `epdg` (`hostname`, `dns_hosts`, `dns_client_subnet`), a rule can carry
`route` (`mcc`, `mnc`), `ike` (`proposal`, `advertise_eap_only`) and `ims`
(transport, codecs, registration headers, …). See
`internal/vowifi/carrier_compat.go` for the full set.

## Two things that will bite you

**1. Your rule must be at least as specific as the built-in one.**
Resolution picks the highest-specificity match, counting how many selector
values a rule matches on — file origin is not a tiebreaker, and a tie goes to
the later-loaded (i.e. your) rule. The built-in Globe rule above matches on
`home_plmns` *and* `iccid_prefixes`; a PLMN-only override loses to it and is
silently ignored. Copy the built-in rule's selectors, then change what you
want. Find them with:

```sh
python3 -c "import json;d=json.load(open('internal/vowifi/carrier_profiles.json'));\
[print(p['id'],m) for p in d['profiles'] for m in [p.get('match',{})]+p.get('match_any',[]) \
 if 'YOUR_PLMN' in (m.get('home_plmns') or [])]"
```

**2. The winning rule replaces, it does not merge.**
The matched rule is applied onto a fresh default profile, so anything the
built-in rule set that you omit reverts to the default. Carry over every field
you still want, not just the one you are changing.

## Validate before restarting

A malformed `.json` aborts startup rather than being skipped, which under
`restart: unless-stopped` becomes a restart loop:

```sh
python3 -m json.tool < data/carrier-profiles.d/your-file.json
```
