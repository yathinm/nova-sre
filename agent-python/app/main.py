from fastapi import FastAPI

from app.clients.github_comments import GitHubPullRequestCommentClient
from app.graph.diagnosis_graph import build_graph
from app.models.schemas import DiagnosisRequest, DiagnosisResponse, DiagnosisState

app = FastAPI(title="Nova-SRE Agent", version="0.1.0")
diagnosis_graph = build_graph()
github_comment_client = GitHubPullRequestCommentClient()


@app.get("/healthz")
async def health() -> dict:
    return {"status": "ok"}


@app.post("/diagnose", response_model=DiagnosisResponse)
async def diagnose(request: DiagnosisRequest) -> DiagnosisResponse:
    result = diagnosis_graph.invoke(request.model_dump(exclude={"post_github_comment"}))
    state = DiagnosisState.model_validate(result)
    should_handle_github_comment = request.post_github_comment or _has_pr_metadata(state)
    if should_handle_github_comment:
        if state.github_owner and state.github_repo and state.github_pr_number:
            comment_result = await github_comment_client.post_comment(
                owner=state.github_owner,
                repo=state.github_repo,
                pr_number=state.github_pr_number,
                body=state.pr_comment,
            )
            state.github_comment_posted = comment_result.posted
            state.github_comment_url = comment_result.url
            state.github_comment_error = comment_result.error
        else:
            state.github_comment_error = (
                "GitHub PR comment posting requires github_owner, github_repo, and "
                "github_pr_number."
            )

    return DiagnosisResponse(
        run_id=state.run_id,
        repo=state.repo,
        sha=state.sha,
        diagnosis=state.diagnosis,
        pr_comment=state.pr_comment,
        github_owner=state.github_owner,
        github_repo=state.github_repo,
        github_pr_number=state.github_pr_number,
        github_comment_posted=state.github_comment_posted,
        github_comment_url=state.github_comment_url,
        github_comment_error=state.github_comment_error,
    )


def _has_pr_metadata(state: DiagnosisState) -> bool:
    return bool(state.github_owner or state.github_repo or state.github_pr_number)
