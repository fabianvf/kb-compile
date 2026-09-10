# Reaches its subject through the package, so no import names engine.py.
# The flattened stem is what recovers the link.
import app


def test_run():
    assert app is not None
