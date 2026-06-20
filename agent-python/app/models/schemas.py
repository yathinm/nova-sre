from pydantic import BaseModel


class DiagnosisState(BaseModel):
    run_id: str
    repo: str
    sha: str
    logs: str = ""
    diagnosis: str = ""
    pr_comment: str = ""
