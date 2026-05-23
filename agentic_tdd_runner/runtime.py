"""Runtime loop orchestration for the agent runner."""

import json
import time
from collections.abc import Callable

from agentic_tdd_runner import completion as _completion
from agentic_tdd_runner import llm as _llm
from agentic_tdd_runner import permission as _permission
from agentic_tdd_runner.state_reviewer import BugStateReviewer


def has_contract_evidence(issue_text: str, episode: dict | None) -> bool:
    """Return whether the model-facing prompt carries concrete contract evidence."""
    text = (issue_text or "").lower()
    issue_has_contract = any(
        token in text
        for token in (
            "callback contract",
            "signature",
            "userstate[",
        )
    )
    if issue_has_contract:
        return True
    if not episode:
        return False
    if episode.get("callback_contracts") or episode.get("dependency_contracts"):
        return True
    cookbook_text = str(episode.get("cookbook_text") or "").lower()
    return any(
        token in cookbook_text
        for token in (
            "callback signature",
            "source emits `",
            "event signature",
            "framework signature",
            "dependency contract",
            "userstate[",
        )
    )


def thinking_phase(
    *,
    step: int,
    test_file_created: bool,
    completion_state: _completion.CompletionState,
) -> str:
    """Map runner state to a small set of budget phases."""
    if completion_state.done_rejected or completion_state.quality_rejected:
        return "recover"
    if test_file_created:
        return "fix"
    if step == 0:
        return "initial"
    return "test"


def run_agent_loop(
    *,
    messages: list[dict],
    episode: dict | None,
    issue_text: str,
    config: dict,
    workdir: str,
    log_path: str,
    emit: Callable[[str], None],
    log: Callable[[str, dict], None],
    chat: Callable,
    execute_tool: Callable[[str, dict], str],
    truncate: Callable[[str], str],
    is_llm_timeout_error: Callable[[Exception], bool],
    tool_applied_status: Callable[[str, str], bool | None],
    tool_loop_signature: Callable[[str, dict], str | None],
    tool_loop_warning_message: Callable[[str], str],
    non_apply_step_warning_message: Callable[[int], str],
    is_test_pass: Callable[[str, dict], bool],
    is_test_file_path: Callable[[str], bool],
    find_test_file: Callable[[str | None], str | None],
    verify_red_green: Callable,
    run_quality_checks: Callable[[str], tuple[bool, str]],
    create_pr: Callable[[list[dict], dict, str, int], str | None],
    clear_file_read_cache: Callable[[], None] | None = None,
) -> int:
    """Run the model/tool/verification loop until DONE or exhaustion."""
    max_steps = config["agent"]["max_steps"]
    emit(f"{'='*60}")
    emit(f"AGENT START — max {max_steps} steps")
    emit(f"Workdir: {workdir}")
    emit(f"Log: {log_path}")
    emit(f"{'='*60}")

    log("start", {"issue": issue_text})

    completion_state = _completion.CompletionState()
    max_rejections = config["verification"]["max_rejections"]
    recent_exploratory_signatures: list[str] = []
    consecutive_non_apply_steps = 0
    test_file_created = False
    permission_grant: str | None = None
    permission_mode = _permission.permission_enabled(config)
    allow_dependency_contract_lookup = False
    block_dependency_contract_lookup = has_contract_evidence(issue_text, episode)
    state_reviewer = BugStateReviewer(
        config,
        contract_evidence_available=block_dependency_contract_lookup,
        runner_facts=(episode or {}).get("runner_facts"),
    )
    non_apply_warning_threshold = int(
        config.get("agent", {}).get("non_apply_step_warning_threshold", 5) or 0
    )

    # --- Completion pipeline: verify, quality, done ---
    # Returns: "done" | "quality_fail" | "verify_fail" | "give_up" | "no_test"
    def try_complete(step, msg: dict):
        return _completion.try_complete(
            step,
            msg,
            messages=messages,
            episode=episode,
            state=completion_state,
            max_rejections=max_rejections,
            config=config,
            workdir=workdir,
            emit=emit,
            log=log,
            find_test_file=find_test_file,
            verify_red_green=verify_red_green,
            run_quality_checks=run_quality_checks,
            create_pr=create_pr,
            clear_file_read_cache=clear_file_read_cache,
        )

    for step in range(max_steps):
        phase = thinking_phase(
            step=step,
            test_file_created=test_file_created,
            completion_state=completion_state,
        )
        permission_context = _permission.build_permission_context(
            episode=episode,
            config=config,
            phase=phase,
            test_file_created=test_file_created,
            workdir=workdir,
        )
        config["_runtime"] = {
            "step": step,
            "max_steps": max_steps,
            "thinking_phase": phase,
            "completion_rejected": bool(
                completion_state.done_rejected or completion_state.quality_rejected
            ),
            "block_dependency_contract_lookup": block_dependency_contract_lookup,
            "allow_dependency_contract_lookup": allow_dependency_contract_lookup,
            "permission_mode": permission_mode,
            "permission_grant": permission_grant,
            "permission_context": permission_context,
        }
        thinking_budget_tokens = _llm.resolve_thinking_budget_tokens(config)

        emit(f"\n>>> Step {step}/{max_steps} — requesting LLM...")
        t0 = time.time()

        try:
            response = chat(messages)
        except Exception as e:
            if is_llm_timeout_error(e):
                emit(f"  LLM TIMEOUT: {e}")
                log("llm_timeout", {
                    "step": step,
                    "error": str(e),
                    "consecutive_non_apply_steps": consecutive_non_apply_steps,
                    "non_apply_warning_threshold": non_apply_warning_threshold,
                })
                return 1
            emit(f"  ERROR: {e}")
            log("error", {"step": step, "error": str(e)})
            return 1

        elapsed = time.time() - t0

        choice = response["choices"][0]
        msg = choice["message"]
        finish = choice["finish_reason"]
        usage = response.get("usage", {})
        completion_state.last_usage = usage
        timings = response.get("timings", {})
        thinking = msg.get("reasoning_content", "")

        log("step", {
            "step": step,
            "elapsed_s": round(elapsed, 1),
            "finish_reason": finish,
            "thinking": thinking,
            "content": msg.get("content", ""),
            "tool_calls": [
                {"name": tc["function"]["name"], "args": tc["function"]["arguments"]}
                for tc in msg.get("tool_calls", [])
            ],
            "usage": usage,
            "timings": {
                "prompt_ms": timings.get("prompt_ms"),
                "predicted_ms": timings.get("predicted_ms"),
                "prompt_per_second": timings.get("prompt_per_second"),
                "predicted_per_second": timings.get("predicted_per_second"),
            },
            "thinking_phase": phase,
            "thinking_budget_tokens": thinking_budget_tokens,
        })

        prompt_tok = usage.get("prompt_tokens", "?")
        comp_tok = usage.get("completion_tokens", "?")
        tok_s = timings.get("predicted_per_second", 0)
        emit(
            f"--- Step {step} | {elapsed:.1f}s | {prompt_tok}→{comp_tok} tok | "
            f"{tok_s:.1f} tok/s | finish={finish} ---"
        )

        if thinking:
            emit(f"  [THINK] ({len(thinking)} chars)")
            for line in thinking.strip().split("\n"):
                emit(f"    {line}")

        if msg.get("content"):
            emit(f"  [SAY] {msg['content']}")
            messages.append(msg)
            if "DONE" in msg["content"].upper():
                completion = try_complete(step, msg)
                if completion == "done":
                    return 0
                if completion == "give_up":
                    return 1
                continue

        # Append assistant message to history
        if not msg.get("content"):
            messages.append(msg)

        if finish == "tool_calls" and msg.get("tool_calls"):
            test_passed = False
            step_had_successful_edit = False
            # OpenAI's tool_calls API requires every assistant(tool_calls) to be
            # followed by a contiguous run of role=tool messages — one per call.
            # If loop detection fires mid-iteration, buffer the warning here and
            # append it AFTER the loop, so siblings stay contiguous instead of
            # producing assistant→tool→user→tool (an invalid transcript).
            post_tool_warnings = []
            for tc in msg["tool_calls"]:
                fn = tc["function"]
                name = fn["name"]
                try:
                    args = json.loads(fn["arguments"])
                except json.JSONDecodeError:
                    args = {}
                    emit(f"  WARNING: failed to parse args: {fn['arguments'][:200]}")

                args_preview = json.dumps(args, ensure_ascii=False)
                if len(args_preview) > 300:
                    args_preview = args_preview[:300] + "..."
                emit(f"  [TOOL] {name}({args_preview})")

                t1 = time.time()
                state_review = None
                permission_review = None
                if permission_mode and name == "ask_harness":
                    permission_review = _permission.answer_harness(args, permission_context)
                    permission_grant = permission_review.grant
                    result = permission_review.message
                    tool_elapsed = 0.0
                    applied = None
                    log(permission_review.event, {
                        "step": step,
                        "intent": args.get("intent"),
                        "grant": permission_grant,
                        "allowed": permission_review.allowed,
                    })
                elif permission_mode:
                    permission_review = _permission.review_tool_call(
                        name,
                        args,
                        grant=permission_grant,
                        context=permission_context,
                        is_test_file_path=is_test_file_path,
                    )
                    if permission_review:
                        result = permission_review.message
                        tool_elapsed = 0.0
                        applied = None
                        log(permission_review.event, {
                            "step": step,
                            "tool": name,
                            "grant": permission_grant,
                            "allowed": permission_review.allowed,
                        })
                    else:
                        state_review = state_reviewer.review_tool_call(
                            name,
                            args,
                            allow_dependency_contract_lookup=allow_dependency_contract_lookup,
                        )
                        if state_review:
                            result = state_review.message
                            tool_elapsed = 0.0
                            applied = None
                            log(state_review.event, {
                                "step": step,
                                **state_review.data,
                            })
                        else:
                            result = execute_tool(name, args)
                            tool_elapsed = time.time() - t1
                            applied = tool_applied_status(name, result)
                            state_reviewer.observe_tool_result(name, args, result, applied=applied)
                            permission_grant = _permission.consume_grant(name, permission_grant)
                else:
                    state_review = state_reviewer.review_tool_call(
                        name,
                        args,
                        allow_dependency_contract_lookup=allow_dependency_contract_lookup,
                    )
                    if state_review:
                        result = state_review.message
                        tool_elapsed = 0.0
                        applied = None
                        log(state_review.event, {
                            "step": step,
                            **state_review.data,
                        })
                    else:
                        result = execute_tool(name, args)
                        tool_elapsed = time.time() - t1
                        applied = tool_applied_status(name, result)
                        state_reviewer.observe_tool_result(name, args, result, applied=applied)
                result_truncated = truncate(result)
                loop_signature = tool_loop_signature(name, args)

                log("tool", {
                    "step": step,
                    "name": name,
                    "args": args,
                    "applied": applied,
                    "result_chars": len(result),
                    "result_truncated": len(result) != len(result_truncated),
                    "elapsed_s": round(tool_elapsed, 3),
                    "result": result_truncated,
                })

                display = result_truncated
                if len(display) > 500:
                    display = (
                        display[:250]
                        + f"\n  ... ({len(result)} chars total) ...\n"
                        + display[-250:]
                    )
                emit(f"  [RESULT] ({len(result)} chars, {tool_elapsed:.2f}s)")
                for line in display.split("\n")[:20]:
                    emit(f"    {line}")
                if display.count("\n") > 20:
                    emit(f"    ... ({display.count(chr(10))} lines total)")

                if (
                    "[Reactive typecheck]" in result
                    or "[Reactive test]" in result
                    or "error TS" in result
                ):
                    allow_dependency_contract_lookup = True

                messages.append({
                    "role": "tool",
                    "tool_call_id": tc["id"],
                    "content": result_truncated,
                })

                if applied is True:
                    step_had_successful_edit = True
                    recent_exploratory_signatures.clear()
                elif loop_signature:
                    recent_exploratory_signatures.append(loop_signature)
                    recent_exploratory_signatures[:] = recent_exploratory_signatures[-3:]
                    if (
                        len(recent_exploratory_signatures) == 3
                        and len(set(recent_exploratory_signatures)) == 1
                    ):
                        loop_warning = tool_loop_warning_message(loop_signature)
                        post_tool_warnings.append(loop_warning)
                        log("loop_detected", {
                            "step": step,
                            "signature": loop_signature,
                            "count": 3,
                        })
                        recent_exploratory_signatures.clear()
                # No else: failed edits and unrelated tools leave the streak
                # intact. Only a successful edit (applied is True) breaks it,
                # because only a successful edit represents actual progress.

                if not state_review and is_test_pass(name, args):
                    test_passed = True

            if step_had_successful_edit:
                consecutive_non_apply_steps = 0
            else:
                consecutive_non_apply_steps += 1
                if (
                    non_apply_warning_threshold > 0
                    and consecutive_non_apply_steps == non_apply_warning_threshold
                ):
                    post_tool_warnings.append(
                        non_apply_step_warning_message(consecutive_non_apply_steps)
                    )
                    log("non_apply_steps_detected", {
                        "step": step,
                        "count": consecutive_non_apply_steps,
                        "threshold": non_apply_warning_threshold,
                    })

            for warning in post_tool_warnings:
                messages.append({"role": "user", "content": warning})

            # --- PHASE NUDGE: test file created → nudge to run + fix ---
            if episode:
                created_test = False
                for tc in msg.get("tool_calls", []):
                    try:
                        fn = tc["function"]
                        if fn["name"] != "create_file":
                            continue
                        tc_args = json.loads(fn["arguments"])
                    except (json.JSONDecodeError, KeyError, TypeError):
                        continue
                    if is_test_file_path(str(tc_args.get("path", ""))):
                        created_test = True
                        break
                if created_test:
                    test_file_created = True
                    nudge = (
                        f"Good. Now run the test to confirm it fails, then fix "
                        f"{episode['source_file']} to make it pass. Say DONE when green."
                    )
                    messages.append({"role": "user", "content": nudge})
                    emit("  [PHASE] Test created → injected run+fix nudge")
                    log("phase_nudge", {"phase": "fix", "test_file": episode["test_file"]})

            # --- AUTO-TRIGGER: test passed → verify → quality → done ---
            if test_passed:
                emit("\n  [AUTO] Test pass detected — triggering verification pipeline")
                completion = try_complete(step, msg)
                if completion == "done":
                    return 0
                if completion == "give_up":
                    return 1
                # quality_fail, verify_fail, no_test → continue loop

        elif finish == "stop":
            if step > 3:
                messages.append({
                    "role": "user",
                    "content": config["prompt"]["nudge"].replace(
                        "{step}", str(step)
                    ).replace("{max_steps}", str(max_steps)),
                })
                emit("  [NUDGE] Continue prompt injected")
        else:
            emit(f"  [FINISH] {finish}")

    emit(f"\n{'='*60}")
    emit(f"AGENT EXHAUSTED — {max_steps} steps without DONE")
    emit(f"{'='*60}")
    log("exhausted", {"steps": max_steps})
    return 1
