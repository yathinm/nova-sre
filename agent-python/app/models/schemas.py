import os
from datetime import datetime
from typing import Any

from pydantic import BaseModel, Field

from app.clients.github_comments import GitHubCommentAction, GitHubCommentMode


DEFAULT_MAX_NORMALIZED_LOG_CHARS = 20_000
MAX_LOG_CHARS_ENV = "NOVA_SRE_MAX_LOG_CHARS"
TRUNCATED_LOG_NOTICE = "[Nova-SRE truncated the submitted logs to fit the diagnosis limit]"


class LogEntry(BaseModel):
    pod: str = ""
    container: str = ""
    logs: str = ""
    error: str = ""


class PullRequestMetadata(BaseModel):
    number: int | None = Field(default=None, gt=0)
    url: str | None = Field(default=None, min_length=1)
    head: str | None = Field(default=None, min_length=1)
    base: str | None = Field(default=None, min_length=1)


class DiagnosisLink(BaseModel):
    label: str
    url: str


class DiagnosisRequestMetadata(BaseModel):
    delivery_id: str | None = None
    event: str | None = None
    repository: str | None = None
    sha: str | None = None
    job_name: str | None = None
    namespace: str | None = None
    reason: str | None = None
    message: str | None = None
    pull_request: PullRequestMetadata | None = None
    observed_time: datetime | None = None
    github_owner: str | None = None
    github_repo: str | None = None
    github_pr_number: int | None = None


class DiagnosisResult(BaseModel):
    status: str
    summary: str
    root_cause: str
    suggested_fix: str
    failure_classification: str
    log_excerpt: str
    links: list[DiagnosisLink] = Field(default_factory=list)
    metadata: DiagnosisRequestMetadata
    llm_used: bool = False
    markdown_valid: bool = False


class DiagnosisState(BaseModel):
    run_id: str
    repo: str
    sha: str
    logs: str = ""
    delivery_id: str | None = None
    event: str | None = None
    job_name: str | None = None
    namespace: str | None = None
    reason: str | None = None
    message: str | None = None
    pull_request: PullRequestMetadata | None = None
    observed_time: datetime | None = None
    github_owner: str | None = None
    github_repo: str | None = None
    github_pr_number: int | None = None
    parsed_logs: list[str] = Field(default_factory=list)
    log_excerpt: str = ""
    failure_classification: str = "unknown"
    error_summary: str = ""
    root_cause_summary: str = ""
    proposed_fix: str = ""
    diagnosis: str = ""
    pr_comment: str = ""
    markdown_valid: bool = False
    markdown_validation_errors: list[str] = Field(default_factory=list)
    llm_used: bool = False
    github_comment_posted: bool = False
    github_comment_url: str | None = None
    github_comment_error: str | None = None
    github_comment_action: GitHubCommentAction = "skipped"


class DiagnosisRequest(BaseModel):
    run_id: str | None = Field(default=None, min_length=1)
    repo: str | None = Field(default=None, min_length=1)
    sha: str | None = Field(default=None, min_length=1)
    logs: str | list[LogEntry] = ""
    delivery_id: str | None = Field(default=None, min_length=1)
    event: str | None = Field(default=None, min_length=1)
    repository: str | None = Field(default=None, min_length=1)
    job_name: str | None = Field(default=None, min_length=1)
    namespace: str | None = Field(default=None, min_length=1)
    reason: str | None = None
    message: str | None = None
    pull_request: PullRequestMetadata | None = None
    observed_time: datetime | None = None
    webhook_body: dict[str, Any] | list[Any] | str | None = None
    github_owner: str | None = Field(default=None, min_length=1)
    github_repo: str | None = Field(default=None, min_length=1)
    github_pr_number: int | None = Field(default=None, gt=0)
    post_github_comment: bool = False
    github_comment_mode: GitHubCommentMode = "upsert"

    def to_state_input(self) -> dict:
        repo = self.repo or self.repository or _repo_from_github_metadata(self)
        run_id = self.run_id or self.delivery_id or self.job_name or "unknown-run"
        pull_request = self.pull_request
        github_pr_number = self.github_pr_number or (pull_request.number if pull_request else None)

        return {
            "run_id": run_id,
            "repo": repo or "unknown-repo",
            "sha": self.sha or (pull_request.head if pull_request else None) or "unknown-sha",
            "logs": _normalize_logs(self.logs),
            "delivery_id": self.delivery_id,
            "event": self.event,
            "job_name": self.job_name,
            "namespace": self.namespace,
            "reason": self.reason,
            "message": self.message,
            "pull_request": pull_request,
            "observed_time": self.observed_time,
            "github_owner": self.github_owner,
            "github_repo": self.github_repo,
            "github_pr_number": github_pr_number,
        }


class DiagnosisResponse(BaseModel):
    run_id: str
    repo: str
    sha: str
    status: str
    summary: str
    root_cause: str
    suggested_fix: str
    result: DiagnosisResult
    diagnosis: str
    pr_comment: str
    github_owner: str | None = None
    github_repo: str | None = None
    github_pr_number: int | None = None
    github_comment_posted: bool = False
    github_comment_url: str | None = None
    github_comment_error: str | None = None
    github_comment_action: GitHubCommentAction = "skipped"


def build_diagnosis_result(state: DiagnosisState) -> DiagnosisResult:
    metadata = DiagnosisRequestMetadata(
        delivery_id=state.delivery_id,
        event=state.event,
        repository=state.repo,
        sha=state.sha,
        job_name=state.job_name,
        namespace=state.namespace,
        reason=state.reason,
        message=state.message,
        pull_request=state.pull_request,
        observed_time=state.observed_time,
        github_owner=state.github_owner,
        github_repo=state.github_repo,
        github_pr_number=state.github_pr_number,
    )
    links = _diagnosis_links(state)
    return DiagnosisResult(
        status=_diagnosis_status(state),
        summary=state.error_summary,
        root_cause=state.root_cause_summary,
        suggested_fix=state.proposed_fix,
        failure_classification=state.failure_classification,
        log_excerpt=state.log_excerpt,
        links=links,
        metadata=metadata,
        llm_used=state.llm_used,
        markdown_valid=state.markdown_valid,
    )


def _normalize_logs(logs: str | list[LogEntry]) -> str:
    if isinstance(logs, str):
        return _truncate_logs(logs)

    blocks: list[str] = []
    for entry in logs:
        prefix_parts = [part for part in (entry.pod, entry.container) if part]
        prefix = f"[{'/'.join(prefix_parts)}] " if prefix_parts else ""
        if entry.logs:
            blocks.extend(f"{prefix}{line}" for line in entry.logs.splitlines() if line.strip())
        if entry.error:
            blocks.append(f"{prefix}ERROR collecting logs: {entry.error}")
    return _truncate_logs("\n".join(blocks))


def _truncate_logs(logs: str) -> str:
    max_chars = _max_normalized_log_chars()
    if len(logs) <= max_chars:
        return logs

    notice = f"\n{TRUNCATED_LOG_NOTICE}\n"
    if max_chars <= len(notice) + 2:
        return notice.strip()[:max_chars]

    remaining = max_chars - len(notice)
    head_chars = remaining // 2
    tail_chars = remaining - head_chars
    return f"{logs[:head_chars]}{notice}{logs[-tail_chars:]}"


def _max_normalized_log_chars() -> int:
    raw = os.getenv(MAX_LOG_CHARS_ENV, "").strip()
    if not raw:
        return DEFAULT_MAX_NORMALIZED_LOG_CHARS
    try:
        parsed = int(raw)
    except ValueError:
        return DEFAULT_MAX_NORMALIZED_LOG_CHARS
    return parsed if parsed > 0 else DEFAULT_MAX_NORMALIZED_LOG_CHARS


def _repo_from_github_metadata(request: DiagnosisRequest) -> str | None:
    if request.github_owner and request.github_repo:
        return f"{request.github_owner}/{request.github_repo}"
    return None


def _diagnosis_status(state: DiagnosisState) -> str:
    if state.failure_classification == "unknown":
        return "needs_more_logs"
    return "identified"


def _diagnosis_links(state: DiagnosisState) -> list[DiagnosisLink]:
    links: list[DiagnosisLink] = []
    if state.pull_request and state.pull_request.url:
        links.append(DiagnosisLink(label="pull_request", url=state.pull_request.url))
    if state.github_owner and state.github_repo and state.github_pr_number:
        links.append(
            DiagnosisLink(
                label="github_pull_request",
                url=(
                    f"https://github.com/{state.github_owner}/{state.github_repo}/pull/"
                    f"{state.github_pr_number}"
                ),
            )
        )
    if state.github_comment_url:
        links.append(DiagnosisLink(label="github_comment", url=state.github_comment_url))
    return links
