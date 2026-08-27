"""Platform-owned runtime for managed Ray Train jobs."""

from .entrypoint import PythonEntrypoint, execute, parse_python_entrypoint

__all__ = ("PythonEntrypoint", "execute", "parse_python_entrypoint")
