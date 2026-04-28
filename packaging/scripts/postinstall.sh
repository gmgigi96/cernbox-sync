#!/bin/sh
if command -v systemctl >/dev/null 2>&1; then
    echo "CERNBox sync daemon installed. To start it automatically at login, run:"
    echo "  systemctl --user enable --now cernbox-syncd"
fi
