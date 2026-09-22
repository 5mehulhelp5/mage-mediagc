#!/bin/sh
# Post-install hook for the .deb / .rpm / .apk packages built by goreleaser.
#
# Nothing here starts a service: mage-mediagc is a tool, not a daemon. The
# timers are installed but deliberately left disabled until an operator has
# filled in /etc/mage-mediagc/mage-mediagc.env, because a scan pointed at the
# wrong database is worse than no scan at all.
set -e

cat <<'EOF'

mage-mediagc installed.

Next steps:

  1. Edit /etc/mage-mediagc/mage-mediagc.env and set MAGEGC_MAGENTO_ROOT
     (and MAGEGC_DB_* if the database is not configured in app/etc/env.php).

  2. Run a read-only scan to confirm it can see your shop:

       mage-mediagc scan --config /etc/mage-mediagc/mage-mediagc.yaml

  3. Optionally enable the safe, automatic maintenance timers:

       systemctl enable --now mage-mediagc-scan.timer    # weekly report
       systemctl enable --now mage-mediagc-cache.timer   # daily cache purge

     Nothing that removes an original image runs on a timer. Quarantining and
     database cleanup are deliberately manual operations.

  Documentation: https://github.com/shuaiZend/mage-mediagc

EOF

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

exit 0
