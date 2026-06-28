import hmac
import os

from fastapi import Depends, FastAPI, Header, HTTPException, status

from app.clients.github_comments import GitHubPullRequestCommentClient
from app.graph.diagnosis_graph import build_graph
from app.models.schemas import (
    DiagnosisRequest,
    DiagnosisResponse,
    DiagnosisState,
    build_diagnosis_result,
)

app = FastAPI(title="Nova-SRE Agent", version="0.1.0")
diagnosis_graph = build_graph()
github_comment_client = GitHubPullRequestCommentClient()


@app.get("/healthz")
async def health() -> dict:
    return {"status": "ok"}


async def require_agent_token(
    x_nova_sre_agent_token: str | None = Header(default=None),
) -> None:
    expected = os.getenv("NOVA_SRE_AGENT_TOKEN", "").strip()
    if not expected:
        return
    candidate = (x_nova_sre_agent_token or "").strip()
    if not candidate or not hmac.compare_digest(candidate, expected):
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="unauthorized",
        )


@app.post("/diagnose", response_model=DiagnosisResponse)
async def diagnose(
    request: DiagnosisRequest,
    _agent_token: None = Depends(require_agent_token),
) -> DiagnosisResponse:
    result = diagnosis_graph.invoke(request.to_state_input())
    state = DiagnosisState.model_validate(result)
    should_handle_github_comment = request.post_github_comment or _has_pr_metadata(state)
    if should_handle_github_comment:
        if state.github_owner and state.github_repo and state.github_pr_number:
            comment_result = await github_comment_client.post_comment(
                owner=state.github_owner,
                repo=state.github_repo,
                pr_number=state.github_pr_number,
                body=state.pr_comment,
                mode=request.github_comment_mode,
            )
            state.github_comment_posted = comment_result.posted
            state.github_comment_url = comment_result.url
            state.github_comment_error = comment_result.error
            state.github_comment_action = comment_result.action
        else:
            state.github_comment_error = (
                "GitHub PR comment posting requires github_owner, github_repo, and "
                "github_pr_number."
            )
            state.github_comment_action = "skipped"

    diagnosis_result = build_diagnosis_result(state)
    return DiagnosisResponse(
        run_id=state.run_id,
        repo=state.repo,
        sha=state.sha,
        status=diagnosis_result.status,
        summary=diagnosis_result.summary,
        root_cause=diagnosis_result.root_cause,
        suggested_fix=diagnosis_result.suggested_fix,
        result=diagnosis_result,
        diagnosis=state.diagnosis,
        pr_comment=state.pr_comment,
        github_owner=state.github_owner,
        github_repo=state.github_repo,
        github_pr_number=state.github_pr_number,
        github_comment_posted=state.github_comment_posted,
        github_comment_url=state.github_comment_url,
        github_comment_error=state.github_comment_error,
        github_comment_action=state.github_comment_action,
    )


def _has_pr_metadata(state: DiagnosisState) -> bool:
    return bool(state.github_owner and state.github_repo and state.github_pr_number)
