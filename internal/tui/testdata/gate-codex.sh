#!/bin/sh
brief=
out=
prev=
for a in "$@"; do
  [ "$prev" = -o ] && out=$a
  prev=$a
  brief=$a
done
skill=ponytail
report='{"test_file":"","changed_files":["done.txt"],"summary":"s","follow_ups":[]}'
case $brief in
*"# Contract review"*)
  skill=ponytail-review
  base=$(printf '%s\n' "$brief" | sed -n 's/^- Base: //p' | head -1)
  head=$(printf '%s\n' "$brief" | sed -n 's/^- HEAD: //p' | head -1)
  report='{"disposition":"approved","base":"'"$base"'","head":"'"$head"'","findings":[],"summary":"s"'
  report=$report',"task_slicing":{"disposition":"ready","reason":"ok","fields":{'
  report=$report'"non_goals":{"status":"present","reason":"n"},'
  report=$report'"invariants":{"status":"present","reason":"i"},'
  report=$report'"acceptance":{"status":"present","reason":"a"},'
  report=$report'"split_triggers":{"status":"present","reason":"s"}},'
  report=$report'"mandatory_pending":[],"drafts":[]}}'
  ;;
*ponytail-review*)
  skill=ponytail-review
  report='{"findings":[],"summary":"Lean already."}'
  ;;
*)
  printf done > done.txt
  ;;
esac
printf '%s\n' "{\"type\":\"item.completed\",\"item\":{\"id\":\"1\",\"type\":\"command_execution\",\"command\":\"cat .agents/skills/$skill/SKILL.md\",\"status\":\"completed\",\"exit_code\":0,\"aggregated_output\":\"---\nname: $skill\n---\n\"}}"
printf '%s\n' "$report" > "$out"
