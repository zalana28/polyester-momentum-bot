#!/bin/zsh
cd "$(dirname "$0")"
touch state-dip/STOP
pkill -INT -f "bin/pmom-dip" 2>/dev/null
pkill -f "run-dip-forever" 2>/dev/null
echo "bot diminta berhenti (flag STOP dipasang)"
