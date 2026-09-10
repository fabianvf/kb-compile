from app.api.handler import handle


def test_handle():
    assert handle({"value": 3})["score"] == 6
