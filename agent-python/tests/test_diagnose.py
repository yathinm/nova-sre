from fastapi.testclient import TestClient

from app.graph.diagnosis_graph import validate_pr_comment
from app.main import app
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
