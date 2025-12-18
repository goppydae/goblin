#!/usr/bin/env python3
"""Shared utilities for lint scripts.

This module consolidates common functionality used across multiple lint scripts:
- Error handling (die function)
- Regex patterns for path validation
- File-finding utilities
- Path validation functions
"""
import re
import sys
from pathlib import Path
from typing import Optional


# Common regex patterns for validation
ABS_PATH_RE = re.compile(r"(^|\s)(/|[A-Za-z]:\\)")
TRUNC_RE = re.compile(r"\.\.\.")
FILE_URL_RE = re.compile(r"file://")


def die(script_name: str, msg: str) -> int:
  """Print error message to stderr and return exit code 1.
  
  Args:
    script_name: Name of the calling script (for error prefix)
    msg: Error message to display
    
  Returns:
    Exit code 1
  """
  print(f"{script_name}: ERROR: {msg}", file=sys.stderr)
  return 1


def find_run_artifact(filename: str) -> Optional[Path]:
  """Find a file under artifacts/history/runs/ hierarchy.
  
  Args:
    filename: Name of the file to find
    
  Returns:
    Path to the first matching file, or None if not found
  """
  runs = Path("artifacts/history/runs")
  if not runs.exists():
    return None
  for p in runs.rglob(filename):
    if p.is_file():
      return p
  return None


def validate_no_file_urls(text: str) -> Optional[str]:
  """Check if text contains file:// URLs.
  
  Args:
    text: Text to validate
    
  Returns:
    Error message if validation fails, None otherwise
  """
  if FILE_URL_RE.search(text):
    return "contains file://; use repo-relative paths only"
  return None


def validate_no_absolute_paths(text: str) -> Optional[str]:
  """Check if text contains absolute paths.
  
  Args:
    text: Text to validate
    
  Returns:
    Error message if validation fails, None otherwise
  """
  if ABS_PATH_RE.search(text):
    return "appears to contain an absolute path; use repo-relative paths only"
  return None


def validate_no_truncation(text: str) -> Optional[str]:
  """Check if text contains ellipsis truncation markers.
  
  Args:
    text: Text to validate
    
  Returns:
    Error message if validation fails, None otherwise
  """
  if TRUNC_RE.search(text):
    return "contains '...'; do not truncate evidence pointers"
  return None


def validate_paths(text: str) -> Optional[str]:
  """Run all path validation checks.
  
  Args:
    text: Text to validate
    
  Returns:
    First error message encountered, or None if all validations pass
  """
  for validator in [validate_no_file_urls, validate_no_absolute_paths, validate_no_truncation]:
    error = validator(text)
    if error:
      return error
  return None
