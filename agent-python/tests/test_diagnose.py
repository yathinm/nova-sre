import httpx
import pytest
from fastapi.testclient import TestClient

from app.clients.github_comments import GitHubPullRequestCommentClient
from app.graph.diagnosis_graph import (
    build_graph,
    classify_failure,
    generate_proposed_fix,
    validate_markdown,
    validate_pr_comment,
)
from app.main import app, github_comment_client
from app.models.schemas import DiagnosisState


def test_diagnose_endpoint_returns_markdown_comment_with_log_block() -> None:
    client = TestClient(app)

    response = client.post(
        "/diagnose",
        json={
            "run_id": "run-123",
            "repo": "acme/nova",
            "sha": "abc123",
            "logs": "booting\nERROR database connection failed\nretry exhausted",
        },
    )

    assert response.status_code == 200
    body = response.json()
    assert body["run_id"] == "run-123"
    assert "database connection failed" in body["diagnosis"]
    assert "## Nova-SRE diagnosis" in body["pr_comment"]
    assert "### Root cause summary" in body["pr_comment"]
    assert "### Proposed fix" in body["pr_comment"]
    assert "```log" in body["pr_comment"]
    assert "ERROR database connection failed" in body["pr_comment"]
    assert body["github_comment_posted"] is False
    assert body["github_comment_url"] is None


def test_diagnose_endpoint_uses_safe_fallback_for_empty_logs() -> None:
    client = TestClient(app)

    response = client.post(
        "/diagnose",
        json={
            "run_id": "run-456",
            "repo": "acme/nova",
            "sha": "def456",
            "logs": "",
        },
    )

    assert response.status_code == 200
    body = response.json()
    assert "No error-like log lines" in body["diagnosis"]
    assert "```log\nNo log excerpt was available for this diagnosis.\n```" in body["pr_comment"]


def test_diagnose_endpoint_neutralizes_nested_fences_in_logs() -> None:
    client = TestClient(app)

    response = client.post(
        "/diagnose",
        json={
            "run_id": "run-567",
            "repo": "acme/nova",
            "sha": "cab567",
            "logs": "ERROR unexpected fenced marker ``` inside logs",
        },
    )

    assert response.status_code == 200
    body = response.json()
    assert "''' inside logs" in body["pr_comment"]
    assert "``` inside logs" not in body["pr_comment"]


def test_validate_pr_comment_appends_safe_fallback_when_block_is_missing() -> None:
    result = validate_pr_comment(
        DiagnosisState(
            run_id="run-789",
            repo="acme/nova",
            sha="fed789",
            diagnosis="precomputed",
            pr_comment="A markdown comment without a fenced block.",
        )
    )

    assert "A markdown comment without a fenced block." in result["pr_comment"]
    assert "```log\nNo log excerpt was available for this diagnosis.\n```" in result["pr_comment"]


def test_classify_failure_detects_dependency_failures() -> None:
    result = classify_failure(
        DiagnosisState(
            run_id="run-deps",
            repo="acme/nova",
            sha="abcdeps",
            parsed_logs=["ERROR Module not found: app.internal.client"],
        )
    )

    assert result["failure_classification"] == "dependency_failure"


def test_generate_proposed_fix_uses_deterministic_fallback(monkeypatch) -> None:
    monkeypatch.setenv("NOVA_SRE_ENABLE_LLM", "false")

    result = generate_proposed_fix(
        DiagnosisState(
            run_id="run-auth",
            repo="acme/nova",
            sha="abcauth",
            failure_classification="auth_failure",
        )
    )

    assert result["llm_used"] is False
    assert "token scope" in result["proposed_fix"]


def test_validate_markdown_marks_complete_comment_valid() -> None:
    result = validate_markdown(
        DiagnosisState(
            run_id="run-md",
            repo="acme/nova",
            sha="abcmd",
            pr_comment=(
                "## Nova-SRE diagnosis\n\n"
                "### Root cause summary\n\n"
                "A runtime exception is causing the failure.\n\n"
                "### Proposed fix\n\n"
                "Reproduce locally.\n\n"
                "```log\nERROR traceback\n```"
            ),
        )
    )

    assert result["markdown_valid"] is True
    assert result["markdown_validation_errors"] == []


def test_graph_retains_offline_fallback_for_unknown_logs(monkeypatch) -> None:
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.setenv("NOVA_SRE_ENABLE_LLM", "true")

    graph = build_graph()
    result = DiagnosisState.model_validate(
        graph.invoke(
            {
                "run_id": "run-unknown",
                "repo": "acme/nova",
                "sha": "abcunknown",
                "logs": "starting\nall quiet\nfinished",
            }
        )
    )

    assert result.failure_classification == "unknown"
    assert result.llm_used is False
    assert "Collect a fuller log excerpt" in result.proposed_fix
    assert result.markdown_valid is True


def test_diagnose_endpoint_posts_github_comment_when_enabled(monkeypatch) -> None:
    requests: list[httpx.Request] = []

    async def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return httpx.Response(
            201,
            json={"html_url": "https://github.com/acme/nova/pull/42#comment"},
        )

    transport = httpx.MockTransport(handler)

    async def post_comment(
        *,
        owner: str,
        repo: str,
        pr_number: int,
        body: str,
    ):
        client = GitHubPullRequestCommentClient(
            token="test-token",
            base_url="https://api.github.test",
        )
        return await client.post_comment(
            owner=owner,
            repo=repo,
            pr_number=pr_number,
            body=body,
            transport=transport,
        )

    monkeypatch.setattr(github_comment_client, "post_comment", post_comment)
    client = TestClient(app)

    response = client.post(
        "/diagnose",
        json={
            "run_id": "run-900",
            "repo": "acme/nova",
            "sha": "abc900",
            "logs": "ERROR deploy failed",
            "github_owner": "acme",
            "github_repo": "nova",
            "github_pr_number": 42,
            "post_github_comment": True,
        },
    )

    assert response.status_code == 200
    body = response.json()
    assert body["github_comment_posted"] is True
    assert body["github_comment_url"] == "https://github.com/acme/nova/pull/42#comment"
    assert body["github_comment_error"] is None
    assert body["github_owner"] == "acme"
    assert body["github_repo"] == "nova"
    assert body["github_pr_number"] == 42
    assert len(requests) == 1
    assert str(requests[0].url) == "https://api.github.test/repos/acme/nova/issues/42/comments"
    assert requests[0].headers["authorization"] == "Bearer test-token"
    assert requests[0].read()
    assert "deploy failed" in requests[0].content.decode()


def test_diagnose_endpoint_posts_github_comment_with_metadata_and_token(monkeypatch) -> None:
    async def post_comment(
        *,
        owner: str,
        repo: str,
        pr_number: int,
        body: str,
    ):
        assert owner == "acme"
        assert repo == "nova"
        assert pr_number == 42
        assert "## Nova-SRE diagnosis" in body
        return type(
            "GitHubResult",
            (),
            {
                "posted": True,
                "url": "https://github.com/acme/nova/pull/42#comment",
                "error": None,
            },
        )()

    monkeypatch.setattr(github_comment_client, "post_comment", post_comment)
    client = TestClient(app)

    response = client.post(
        "/diagnose",
        json={
            "run_id": "run-902",
            "repo": "acme/nova",
            "sha": "abc902",
            "logs": "ERROR deploy failed",
            "github_owner": "acme",
            "github_repo": "nova",
            "github_pr_number": 42,
        },
    )

    assert response.status_code == 200
    body = response.json()
    assert body["github_comment_posted"] is True
    assert body["github_comment_url"] == "https://github.com/acme/nova/pull/42#comment"
    assert body["github_comment_error"] is None


def test_diagnose_endpoint_dry_runs_github_comment_without_token(monkeypatch) -> None:
    monkeypatch.setattr(github_comment_client, "token", None)
    client = TestClient(app)

    response = client.post(
        "/diagnose",
        json={
            "run_id": "run-901",
            "repo": "acme/nova",
            "sha": "abc901",
            "logs": "ERROR deploy failed",
            "github_owner": "acme",
            "github_repo": "nova",
            "github_pr_number": 42,
            "post_github_comment": True,
        },
    )

    assert response.status_code == 200
    body = response.json()
    assert body["github_comment_posted"] is False
    assert body["github_comment_url"] is None
    assert body["github_comment_error"] == (
        "GITHUB_TOKEN is not set; skipped GitHub PR comment posting."
    )


def test_diagnose_endpoint_dry_runs_github_comment_without_metadata() -> None:
    client = TestClient(app)

    response = client.post(
        "/diagnose",
        json={
            "run_id": "run-903",
            "repo": "acme/nova",
            "sha": "abc903",
            "logs": "ERROR deploy failed",
            "post_github_comment": True,
        },
    )

    assert response.status_code == 200
    body = response.json()
    assert body["github_comment_posted"] is False
    assert body["github_comment_url"] is None
    assert body["github_comment_error"] == (
        "GitHub PR comment posting requires github_owner, github_repo, and github_pr_number."
    )


def test_diagnose_endpoint_keeps_api_failure_nonfatal(monkeypatch) -> None:
    async def post_comment(
        *,
        owner: str,
        repo: str,
        pr_number: int,
        body: str,
    ):
        return type(
            "GitHubResult",
            (),
            {
                "posted": False,
                "url": None,
                "error": "GitHub PR comment posting failed with HTTP 500.",
            },
        )()

    monkeypatch.setattr(github_comment_client, "post_comment", post_comment)
    client = TestClient(app)

    response = client.post(
        "/diagnose",
        json={
            "run_id": "run-904",
            "repo": "acme/nova",
            "sha": "abc904",
            "logs": "ERROR deploy failed",
            "github_owner": "acme",
            "github_repo": "nova",
            "github_pr_number": 42,
        },
    )

    assert response.status_code == 200
    body = response.json()
    assert body["github_comment_posted"] is False
    assert body["github_comment_url"] is None
    assert body["github_comment_error"] == "GitHub PR comment posting failed with HTTP 500."


@pytest.mark.asyncio
async def test_github_comment_client_dry_runs_without_token(monkeypatch) -> None:
    monkeypatch.delenv("GITHUB_TOKEN", raising=False)
    client = GitHubPullRequestCommentClient()

    result = await client.post_comment(
        owner="acme",
        repo="nova",
        pr_number=42,
        body="comment body",
    )

    assert result.posted is False
    assert result.url is None
    assert result.error == "GITHUB_TOKEN is not set; skipped GitHub PR comment posting."


@pytest.mark.asyncio
async def test_github_comment_client_handles_api_failure() -> None:
    async def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(500, json={"message": "server error"})

    client = GitHubPullRequestCommentClient(
        token="test-token",
        base_url="https://api.github.test",
    )

    result = await client.post_comment(
        owner="acme",
        repo="nova",
        pr_number=42,
        body="comment body",
        transport=httpx.MockTransport(handler),
    )

    assert result.posted is False
    assert result.url is None
    assert result.error == "GitHub PR comment posting failed with HTTP 500."


@pytest.mark.asyncio
async def test_github_comment_client_handles_transport_failure() -> None:
    async def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("network unavailable", request=request)

    client = GitHubPullRequestCommentClient(
        token="test-token",
        base_url="https://api.github.test",
    )

    result = await client.post_comment(
        owner="acme",
        repo="nova",
        pr_number=42,
        body="comment body",
        transport=httpx.MockTransport(handler),
    )

    assert result.posted is False
    assert result.url is None
    assert result.error == "GitHub PR comment posting failed: network unavailable"
