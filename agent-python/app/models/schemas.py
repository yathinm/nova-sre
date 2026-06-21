from pydantic import BaseModel, Field


class DiagnosisState(BaseModel):
    run_id: str
    repo: str
    sha: str
    logs: str = ""
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


class DiagnosisRequest(BaseModel):
    run_id: str = Field(..., min_length=1)
    repo: str = Field(..., min_length=1)
    sha: str = Field(..., min_length=1)
    logs: str = ""
    github_owner: str | None = Field(default=None, min_length=1)
    github_repo: str | None = Field(default=None, min_length=1)
    github_pr_number: int | None = Field(default=None, gt=0)
    post_github_comment: bool = False


class DiagnosisResponse(BaseModel):
    run_id: str
    repo: str
    sha: str
    diagnosis: str
    pr_comment: str
    github_owner: str | None = None
    github_repo: str | None = None
    github_pr_number: int | None = None
    github_comment_posted: bool = False
    github_comment_url: str | None = None
    github_comment_error: str | None = None
