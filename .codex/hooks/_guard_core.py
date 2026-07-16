#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Codex 侧共享 Local Action Firewall 离线判定核心。

实际逻辑统一复用 `.claude/hooks/_guard_core.py`，避免两份 hook 演化成
不同的高危判定口径。
"""

from __future__ import annotations

import importlib.util
from pathlib import Path

# 这里硬依赖仓库内 Claude 侧固定相对路径；单独部署 Codex hook 且缺少该文件会 ImportError。
_CORE_PATH = Path(__file__).parents[2] / ".claude" / "hooks" / "_guard_core.py"
_SPEC = importlib.util.spec_from_file_location("_agenttoolgate_shared_guard_core", _CORE_PATH)
if _SPEC is None or _SPEC.loader is None:
    raise ImportError(f"cannot load shared guard core from {_CORE_PATH}")
_MODULE = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(_MODULE)

is_probably_script_target = _MODULE.is_probably_script_target
path_segments = _MODULE.path_segments
has_suffix = _MODULE.has_suffix
has_sequence = _MODULE.has_sequence
path_matches_exact_file = _MODULE.path_matches_exact_file
path_matches_dir_or_descendant = _MODULE.path_matches_dir_or_descendant
is_probably_high_risk_target = _MODULE.is_probably_high_risk_target
contains_hidden_script_features = _MODULE.contains_hidden_script_features
decoded_base64_payloads = _MODULE.decoded_base64_payloads
contains_hidden_script_features_in_decoded_base64 = _MODULE.contains_hidden_script_features_in_decoded_base64
is_high_risk_offline_target = _MODULE.is_high_risk_offline_target

__all__ = [
    "contains_hidden_script_features",
    "contains_hidden_script_features_in_decoded_base64",
    "decoded_base64_payloads",
    "has_sequence",
    "has_suffix",
    "is_high_risk_offline_target",
    "is_probably_high_risk_target",
    "is_probably_script_target",
    "path_matches_dir_or_descendant",
    "path_matches_exact_file",
    "path_segments",
]
