from langgraph.graph import StateGraph, END
from agent_python.app.models.schemas import DiagnosisState


def build_graph() -> StateGraph:
    graph = StateGraph(DiagnosisState)
    # Nodes will be added incrementally per feature branch.
    return graph.compile()
