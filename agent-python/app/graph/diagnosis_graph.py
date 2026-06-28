import os

from langgraph.graph import END, StateGraph

from app.clients.github_comments import NOVA_SRE_COMMENT_MARKER
from app.models.schemas import DiagnosisState


ERROR_MARKERS = (
    "error",
    "exception",
    "failed",
    "failure",
    "fatal",
    "traceback",
    "panic",
)
FAILURE_PATTERNS = (
    (
        "test_failure",
        (
            "assert",
            "pytest",
            "test failed",
            "tests failed",
            "failed test",
            "failed tests/",
            "jest",
            "rspec",
        ),
        "A test failure is blocking the run.",
        "Re-run the failing test locally, inspect the assertion diff, and update either the "
        "code or the test fixture that no longer matches expected behavior.",
    ),
    (
        "lint_failure",
        ("ruff", "flake8", "eslint", "golangci-lint", "lint failed", "format"),
        "A lint or formatting check is failing.",
        "Run the same lint command locally and apply the formatter or address the reported "
        "rule violations before retrying CI.",
    ),
    (
        "dependency_failure",
        (
            "no matching distribution",
            "module not found",
            "cannot find module",
            "importerror",
            "npm err",
            "package not found",
        ),
        "A dependency or package resolution problem is blocking the run.",
        "Verify the dependency name and version constraints, refresh the lockfile if needed, "
        "and confirm the CI environment can access the configured package registry.",
    ),
    (
        "timeout",
        ("timed out", "timeout", "deadline exceeded", "context deadline"),
        "The run appears to have exceeded a timeout or deadline.",
        "Look for the slow operation in the preceding logs, then either reduce its runtime, "
        "add retries around transient calls, or raise the timeout with evidence.",
    ),
    (
        "auth_failure",
        ("unauthorized", "forbidden", "permission denied", "access denied", "401", "403"),
        "An authorization or credential problem is blocking the run.",
        "Check that the CI secret, token scope, and target resource permissions match the "
        "operation that failed.",
    ),
    (
        "deploy_failure",
        ("deploy failed", "rollout failed", "helm", "kubectl", "terraform apply"),
        "A deployment step failed.",
        "Inspect the deployment tool output immediately above the error, verify the target "
        "environment is healthy, and retry only after the reported resource issue is addressed.",
    ),
    (
        "runtime_failure",
        ("traceback", "exception", "panic", "segmentation fault", "fatal"),
        "A runtime exception or crash is causing the failure.",
        "Use the stack trace to identify the first application frame, reproduce that path "
        "locally, and add a regression test around the failing input.",
    ),
)
SAFE_FALLBACK_BLOCK = "```log\nNo log excerpt was available for this diagnosis.\n```"
LLM_ENABLED_ENV = "NOVA_SRE_ENABLE_LLM"
LLM_MODEL_ENV = "NOVA_SRE_LLM_MODEL"


def neutralize_markdown_fences(value: str) -> str:
    return value.replace("```", "'''")


def format_log_block(excerpt: str) -> str:
    safe_excerpt = neutralize_markdown_fences(excerpt).strip()
    return f"```log\n{safe_excerpt}\n```" if safe_excerpt else SAFE_FALLBACK_BLOCK


def parse_logs(state: DiagnosisState) -> dict:
    lines = [line.strip() for line in state.logs.splitlines() if line.strip()]
    relevant_lines = [
        line for line in lines if any(marker in line.lower() for marker in ERROR_MARKERS)
    ]
    parsed_logs = relevant_lines[:20] or lines[-20:]
    log_excerpt = "\n".join(parsed_logs[:10]).strip()

    return {
        "parsed_logs": parsed_logs,
        "log_excerpt": neutralize_markdown_fences(log_excerpt),
    }


def classify_failure(state: DiagnosisState) -> dict:
    searchable_text = "\n".join(state.parsed_logs).lower()
    for classification, markers, _summary, _fix in FAILURE_PATTERNS:
        if any(marker in searchable_text for marker in markers):
            return {"failure_classification": classification}

    if any(marker in searchable_text for marker in ERROR_MARKERS):
        return {"failure_classification": "generic_failure"}

    return {"failure_classification": "unknown"}


def summarize_root_cause(state: DiagnosisState) -> dict:
    if state.parsed_logs:
        summary_source = state.parsed_logs[0]
        error_summary = neutralize_markdown_fences(summary_source[:240])
    else:
        error_summary = "No error-like log lines were found in the submitted logs."

    classification_summary = _classification_summary(state.failure_classification)
    root_cause_summary = f"{classification_summary} Evidence: {error_summary}"
    diagnosis = (
        f"Run {state.run_id} for {state.repo}@{state.sha} appears to be failing with: "
        f"{root_cause_summary}"
    )
    return {
        "error_summary": error_summary,
        "root_cause_summary": root_cause_summary,
        "diagnosis": diagnosis,
    }


def generate_proposed_fix(state: DiagnosisState) -> dict:
    llm_fix = _generate_llm_fix(state)
    if llm_fix:
        return {"proposed_fix": llm_fix, "llm_used": True}

    return {
        "proposed_fix": _classification_fix(state.failure_classification),
        "llm_used": False,
    }


def generate_pr_comment(state: DiagnosisState) -> dict:
    log_block = format_log_block(state.log_excerpt)
    comment = (
        "## Nova-SRE diagnosis\n\n"
        f"**Run:** `{state.run_id}`\n"
        f"**Commit:** `{state.sha}`\n\n"
        f"**Failure class:** `{state.failure_classification}`\n\n"
        f"### Root cause summary\n\n"
        f"{state.root_cause_summary}\n\n"
        "### Proposed fix\n\n"
        f"{state.proposed_fix}\n\n"
        "### Relevant log excerpt\n\n"
        f"{log_block}\n\n"
        f"{NOVA_SRE_COMMENT_MARKER}\n\n"
        "_Generated by Nova-SRE agent foundation flow._"
    )

    return {"pr_comment": comment}


def validate_markdown(state: DiagnosisState) -> dict:
    errors = _markdown_validation_errors(state.pr_comment)
    if not errors:
        return {"markdown_valid": True, "markdown_validation_errors": []}

    pr_comment = state.pr_comment
    if "missing supported fenced log block" in errors:
        pr_comment = (
            pr_comment.rstrip()
            + "\n\n### Safe fallback log excerpt\n\n"
            + SAFE_FALLBACK_BLOCK
        )

    remaining_errors = _markdown_validation_errors(pr_comment)
    return {
        "pr_comment": pr_comment,
        "markdown_valid": not remaining_errors,
        "markdown_validation_errors": remaining_errors,
    }


def validate_pr_comment(state: DiagnosisState) -> dict:
    return validate_markdown(state)


def _markdown_validation_errors(comment: str) -> list[str]:
    errors: list[str] = []
    has_fenced_block = "```" in comment
    has_supported_block = "```log" in comment or "```text" in comment
    if "## Nova-SRE diagnosis" not in comment:
        errors.append("missing diagnosis heading")
    if "### Root cause summary" not in comment:
        errors.append("missing root cause section")
    if "### Proposed fix" not in comment:
        errors.append("missing proposed fix section")
    if not has_fenced_block or not has_supported_block:
        errors.append("missing supported fenced log block")
    if comment.count("```") % 2 != 0:
        errors.append("unbalanced fenced code blocks")
    return errors


def _classification_summary(classification: str) -> str:
    for known_classification, _markers, summary, _fix in FAILURE_PATTERNS:
        if classification == known_classification:
            return summary

    if classification == "generic_failure":
        return (
            "The logs include error-like lines, but the failure does not match a known "
            "category yet."
        )

    return "No concrete failure category could be inferred from the submitted logs."


def _classification_fix(classification: str) -> str:
    for known_classification, _markers, _summary, fix in FAILURE_PATTERNS:
        if classification == known_classification:
            return fix

    if classification == "generic_failure":
        return (
            "Start with the first error-like line, inspect the surrounding job step, and add "
            "a narrower classifier once the recurring signature is understood."
        )

    return (
        "Collect a fuller log excerpt from the failed job and retry diagnosis once the failing "
        "command emits a concrete error."
    )


def _generate_llm_fix(state: DiagnosisState) -> str | None:
    if os.getenv(LLM_ENABLED_ENV, "").lower() not in {"1", "true", "yes"}:
        return None
    if not os.getenv("OPENAI_API_KEY"):
        return None

    try:
        from langchain_openai import ChatOpenAI
    except ImportError:
        return None

    prompt = (
        "You are Nova-SRE. Propose one concise remediation for this CI failure. "
        "Do not use markdown fences.\n\n"
        f"Failure class: {state.failure_classification}\n"
        f"Root cause: {state.root_cause_summary}\n"
        f"Logs:\n{state.log_excerpt}"
    )
    model = ChatOpenAI(model=os.getenv(LLM_MODEL_ENV, "gpt-4o-mini"), temperature=0)
    try:
        response = model.invoke(prompt)
    except Exception:
        return None
    content = getattr(response, "content", "")
    if not isinstance(content, str):
        return None

    proposed_fix = neutralize_markdown_fences(content).strip()
    return proposed_fix or None


def build_graph() -> StateGraph:
    graph = StateGraph(DiagnosisState)
    graph.add_node("parse_logs", parse_logs)
    graph.add_node("classify_failure", classify_failure)
    graph.add_node("summarize_root_cause", summarize_root_cause)
    graph.add_node("generate_proposed_fix", generate_proposed_fix)
    graph.add_node("generate_pr_comment", generate_pr_comment)
    graph.add_node("validate_markdown", validate_markdown)

    graph.set_entry_point("parse_logs")
    graph.add_edge("parse_logs", "classify_failure")
    graph.add_edge("classify_failure", "summarize_root_cause")
    graph.add_edge("summarize_root_cause", "generate_proposed_fix")
    graph.add_edge("generate_proposed_fix", "generate_pr_comment")
    graph.add_edge("generate_pr_comment", "validate_markdown")
    graph.add_edge("validate_markdown", END)

    return graph.compile()
