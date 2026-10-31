#!/usr/bin/env python3
"""Validate the small checked-in manifest schema without external packages."""

from __future__ import annotations

import datetime
import json
import pathlib
import re
import sys


def resolve_ref(root: dict, reference: str) -> dict:
    if not reference.startswith("#/"):
        raise ValueError(f"unsupported schema reference {reference}")
    value = root
    for component in reference[2:].split("/"):
        value = value[component.replace("~1", "/").replace("~0", "~")]
    return value


def validate(value, schema: dict, root: dict, location: str = "$") -> None:
    if "$ref" in schema:
        validate(value, resolve_ref(root, schema["$ref"]), root, location)
        return
    for index, clause in enumerate(schema.get("allOf", [])):
        validate(value, clause, root, f"{location}.allOf[{index}]")
    if "if" in schema:
        try:
            validate(value, schema["if"], root, f"{location}.if")
        except ValueError:
            branch = schema.get("else")
            branch_name = "else"
        else:
            branch = schema.get("then")
            branch_name = "then"
        if branch is not None:
            validate(value, branch, root, f"{location}.{branch_name}")
    if "const" in schema and value != schema["const"]:
        raise ValueError(f"{location}: value does not match const")
    if "enum" in schema and value not in schema["enum"]:
        raise ValueError(f"{location}: value is not in enum")

    expected = schema.get("type")
    type_ok = {
        "object": isinstance(value, dict),
        "array": isinstance(value, list),
        "string": isinstance(value, str),
        "integer": isinstance(value, int) and not isinstance(value, bool),
        "boolean": isinstance(value, bool),
    }.get(expected, True)
    if not type_ok:
        raise ValueError(f"{location}: expected {expected}")

    if isinstance(value, dict):
        properties = schema.get("properties", {})
        missing = set(schema.get("required", [])) - value.keys()
        if missing:
            raise ValueError(f"{location}: missing required fields {sorted(missing)}")
        if schema.get("additionalProperties") is False:
            extra = value.keys() - properties.keys()
            if extra:
                raise ValueError(f"{location}: unexpected fields {sorted(extra)}")
        for key, item in value.items():
            if key in properties:
                validate(item, properties[key], root, f"{location}.{key}")
    elif isinstance(value, list):
        if "minItems" in schema and len(value) < schema["minItems"]:
            raise ValueError(f"{location}: too few items")
        if "items" in schema:
            for index, item in enumerate(value):
                validate(item, schema["items"], root, f"{location}[{index}]")
    elif isinstance(value, str):
        if "pattern" in schema and re.search(schema["pattern"], value) is None:
            raise ValueError(f"{location}: string does not match pattern")
        if schema.get("format") == "date-time":
            try:
                datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
            except ValueError as error:
                raise ValueError(f"{location}: invalid date-time") from error
    elif isinstance(value, int) and "minimum" in schema and value < schema["minimum"]:
        raise ValueError(f"{location}: value is below minimum")


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("usage: validate-json-schema.py SCHEMA DOCUMENT")
    schema = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
    document = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
    try:
        validate(document, schema, schema)
    except ValueError as error:
        raise SystemExit(f"JSON schema validation failed: {error}") from error


if __name__ == "__main__":
    main()
