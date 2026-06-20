from pydantic import BaseModel, Field


class DiagnosisState(BaseModel):
    run_id: str
    repo: str
    sha: str
    logs: str = ""
    parsed_logs: list[str] = Field(default_factory=list)
    error_summary: str = ""
    diagnosis: str = ""
    pr_comment: str = ""


class DiagnosisRequest(BaseModel):
    run_id: str = Field(..., min_length=1)
    repo: str = Field(..., min_length=1)
    sha: str = Field(..., min_length=1)
    logs: str = ""


class DiagnosisResponse(BaseModel):
    run_id: str
    repo: str
    sha: str
    diagnosis: str
    pr_comment: str
