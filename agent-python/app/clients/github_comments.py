import os
from dataclasses import dataclass
from typing import Any, Literal
from urllib.parse import quote

import httpx


GITHUB_API_BASE_URL = "https://api.github.com"
MAX_GITHUB_ERROR_DETAIL_CHARS = 240
NOVA_SRE_COMMENT_MARKER = "<!-- nova-sre:diagnosis -->"

GitHubCommentAction = Literal["created", "updated", "skipped", "failed"]
GitHubCommentMode = Literal["upsert", "create"]


@dataclass(frozen=True)
class GitHubCommentResult:
    posted: bool
    url: str | None = None
    error: str | None = None
    action: GitHubCommentAction = "skipped"


class GitHubPullRequestCommentClient:
    def __init__(
        self,
        *,
        token: str | None = None,
        base_url: str = GITHUB_API_BASE_URL,
        timeout: float = 10.0,
    ) -> None:
        self.token = token if token is not None else os.getenv("GITHUB_TOKEN")
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    async def post_comment(
        self,
        *,
        owner: str,
        repo: str,
        pr_number: int,
        body: str,
        mode: GitHubCommentMode = "upsert",
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> GitHubCommentResult:
        if not self.token:
            return GitHubCommentResult(
                posted=False,
                error="GITHUB_TOKEN is not set; skipped GitHub PR comment posting.",
                action="skipped",
            )

        comments_url = self._issue_comments_url(owner=owner, repo=repo, pr_number=pr_number)
        headers = {
            "Accept": "application/vnd.github+json",
            "Authorization": f"Bearer {self.token}",
            "X-GitHub-Api-Version": "2022-11-28",
        }

        async with httpx.AsyncClient(
            base_url=self.base_url,
            timeout=self.timeout,
            transport=transport,
        ) as client:
            if mode == "upsert":
                existing = await self._find_existing_comment(
                    client=client,
                    url=comments_url,
                    headers=headers,
                )
                if existing.error:
                    return GitHubCommentResult(
                        posted=False,
                        error=existing.error,
                        action="failed",
                    )
                if existing.comment_id is not None:
                    return await self._update_comment(
                        client=client,
                        owner=owner,
                        repo=repo,
                        comment_id=existing.comment_id,
                        headers=headers,
                        body=body,
                    )

            try:
                response = await client.post(comments_url, headers=headers, json={"body": body})
            except httpx.HTTPError as exc:
                return GitHubCommentResult(
                    posted=False,
                    error=f"GitHub PR comment posting failed: {exc}",
                    action="failed",
                )

        if response.is_success:
            try:
                payload = response.json()
            except ValueError:
                payload = {}
            return GitHubCommentResult(
                posted=True,
                url=payload.get("html_url"),
                action="created",
            )

        return GitHubCommentResult(
            posted=False,
            error=github_error_message(response, operation="posting"),
            action="failed",
        )

    async def _find_existing_comment(
        self,
        *,
        client: httpx.AsyncClient,
        url: str,
        headers: dict[str, str],
    ) -> "_ExistingCommentResult":
        try:
            response = await client.get(url, headers=headers, params={"per_page": 100})
        except httpx.HTTPError as exc:
            return _ExistingCommentResult(
                error=f"GitHub PR comment lookup failed: {exc}",
            )

        if not response.is_success:
            return _ExistingCommentResult(
                error=github_error_message(response, operation="lookup"),
            )

        try:
            payload = response.json()
        except ValueError:
            payload = []

        if not isinstance(payload, list):
            return _ExistingCommentResult()

        for comment in reversed(payload):
            if not isinstance(comment, dict):
                continue
            body = str(comment.get("body") or "")
            if NOVA_SRE_COMMENT_MARKER not in body:
                continue
            comment_id = comment.get("id")
            if isinstance(comment_id, int):
                return _ExistingCommentResult(
                    comment_id=comment_id,
                    url=str(comment.get("html_url") or "") or None,
                )
        return _ExistingCommentResult()

    async def _update_comment(
        self,
        *,
        client: httpx.AsyncClient,
        owner: str,
        repo: str,
        comment_id: int,
        headers: dict[str, str],
        body: str,
    ) -> GitHubCommentResult:
        url = self._comment_url(owner=owner, repo=repo, comment_id=comment_id)
        try:
            response = await client.patch(url, headers=headers, json={"body": body})
        except httpx.HTTPError as exc:
            return GitHubCommentResult(
                posted=False,
                error=f"GitHub PR comment update failed: {exc}",
                action="failed",
            )

        if response.is_success:
            try:
                payload = response.json()
            except ValueError:
                payload = {}
            return GitHubCommentResult(
                posted=True,
                url=payload.get("html_url"),
                action="updated",
            )

        return GitHubCommentResult(
            posted=False,
            error=github_error_message(response, operation="update"),
            action="failed",
        )

    def _issue_comments_url(self, *, owner: str, repo: str, pr_number: int) -> str:
        return (
            f"{self.base_url}/repos/{_path_component(owner)}/{_path_component(repo)}"
            f"/issues/{pr_number}/comments"
        )

    def _comment_url(self, *, owner: str, repo: str, comment_id: int) -> str:
        return (
            f"{self.base_url}/repos/{_path_component(owner)}/{_path_component(repo)}"
            f"/issues/comments/{comment_id}"
        )


@dataclass(frozen=True)
class _ExistingCommentResult:
    comment_id: int | None = None
    url: str | None = None
    error: str | None = None


def github_error_message(response: httpx.Response, *, operation: str = "posting") -> str:
    detail = _github_error_detail(response)
    message = f"GitHub PR comment {operation} failed with HTTP {response.status_code}"
    if response.status_code in (401, 403):
        message += "; check GITHUB_TOKEN permissions for issue comments"
    if detail:
        message += f": {detail}"
    return f"{message}."


def _github_error_detail(response: httpx.Response) -> str:
    try:
        payload: Any = response.json()
    except ValueError:
        payload = response.text

    if isinstance(payload, dict):
        detail = str(payload.get("message") or "")
        errors = payload.get("errors")
        if isinstance(errors, list) and errors:
            detail = " ".join(part for part in (detail, _summarize_errors(errors)) if part)
    else:
        detail = str(payload or "")

    detail = " ".join(detail.split())
    if len(detail) > MAX_GITHUB_ERROR_DETAIL_CHARS:
        detail = detail[: MAX_GITHUB_ERROR_DETAIL_CHARS - 3].rstrip() + "..."
    return detail


def _summarize_errors(errors: list[Any]) -> str:
    parts: list[str] = []
    for error in errors[:3]:
        if isinstance(error, dict):
            field = error.get("field")
            code = error.get("code")
            message = error.get("message")
            parts.append(
                " ".join(str(part) for part in (field, code, message) if part)
            )
        else:
            parts.append(str(error))
    return "; ".join(parts)


def _path_component(value: str) -> str:
    return quote(value.strip(), safe="")
