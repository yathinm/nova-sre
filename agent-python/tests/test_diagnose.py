import httpx
import pytest
from fastapi.testclient import TestClient

from app.clients.github_comments import GitHubPullRequestCommentClient
from app.graph.diagnosis_graph import validate_pr_comment
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
