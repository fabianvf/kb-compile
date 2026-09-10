from app.core.engine import run


def handle(req):
    return {"score": run(req["value"])}
