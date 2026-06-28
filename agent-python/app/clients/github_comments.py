import os
from dataclasses import dataclass

import httpx


GITHUB_API_BASE_URL = "https://api.github.com"


@dataclass(frozen=True)
class GitHubCommentResult:
    posted: bool
    url: str | None = None
    error: str | None = None


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
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> GitHubCommentResult:
        if not self.token:
            return GitHubCommentResult(
                posted=False,
                error="GITHUB_TOKEN is not set; skipped GitHub PR comment posting.",
            )

        url = f"{self.base_url}/repos/{owner}/{repo}/issues/{pr_number}/comments"
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
            try:
                response = await client.post(url, headers=headers, json={"body": body})
            except httpx.HTTPError as exc:
                return GitHubCommentResult(
                    posted=False,
                    error=f"GitHub PR comment posting failed: {exc}",
                )

        if response.is_success:
            try:
                payload = response.json()
            except ValueError:
                payload = {}
            return GitHubCommentResult(posted=True, url=payload.get("html_url"))

        return GitHubCommentResult(
            posted=False,
            error=f"GitHub PR comment posting failed with HTTP {response.status_code}.",
        )
