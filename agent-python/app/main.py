from fastapi import FastAPI

app = FastAPI(title="Nova-SRE Agent", version="0.1.0")


@app.get("/healthz")
async def health() -> dict:
    return {"status": "ok"}
