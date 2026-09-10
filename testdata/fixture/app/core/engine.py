"""Scoring engine."""
from app.core import util


def run(x):
    return util.clamp(x * 2)
