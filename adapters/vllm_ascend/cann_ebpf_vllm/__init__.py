"""vLLM general plugin that emits batch/request identifiers to cann-ebpf."""

from .plugin import register

__all__ = ["register"]
