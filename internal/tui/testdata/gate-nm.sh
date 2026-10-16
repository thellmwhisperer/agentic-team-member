#!/bin/sh
case "$1 $2" in
"axi run") exit 0 ;;
"axi status") ;;
*) exit 2 ;;
esac
n=0
[ -f "$ATM_GATE_DIR/n" ] && n=$(cat "$ATM_GATE_DIR/n")
n=$((n+1))
printf '%s\n' "$n" > "$ATM_GATE_DIR/n"
name=failed
[ "$n" = 1 ] && name=running
cat "$ATM_GATE_DIR/$name"
