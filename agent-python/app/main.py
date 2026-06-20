from fastapi import FastAPI

from app.graph.diagnosis_graph import build_graph
from app.models.schemas import DiagnosisRequest, DiagnosisResponse, DiagnosisState

app = FastAPI(title="Nova-SRE Agent", version="0.1.0")
diagnosis_graph = build_graph()


@app.get("/healthz")
async def health() -> dict:
    return {"status": "ok"}


@app.post("/diagnose", response_model=DiagnosisResponse)
async def diagnose(request: DiagnosisRequest) -> DiagnosisResponse:
    result = diagnosis_graph.invoke(request.model_dump())
    state = DiagnosisState.model_validate(result)
    return DiagnosisResponse(
        run_id=state.run_id,
        repo=state.repo,
        sha=state.sha,
        diagnosis=state.diagnosis,
        pr_comment=state.pr_comment,
    )
