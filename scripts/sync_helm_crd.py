#!/usr/bin/env python3
# Copyright 2023 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""
Sync CRD from generated CRD to Helm chart template.

This script extracts the CRD schema from the generated CRD file
(config/crd/bases/autoscaling.x-k8s.io_startupcpuboosts.yaml)
and updates the Helm chart CRD template
(charts/kube-startup-cpu-boost/templates/startupcpuboost-crd.yaml)
while preserving Helm-specific metadata (labels, annotations, template syntax).
"""

import sys
import yaml
import re
from pathlib import Path


def load_yaml(file_path):
    """Load YAML file."""
    with open(file_path, 'r') as f:
        return yaml.safe_load(f)


def sync_crd_schema(generated_crd_path, helm_crd_path):
    """Sync CRD schema from generated CRD to Helm chart template."""
    # Load generated CRD
    generated_crd = load_yaml(generated_crd_path)
    
    # Extract the schema from generated CRD
    generated_spec = generated_crd.get('spec', {})
    generated_versions = generated_spec.get('versions', [])
    
    if not generated_versions:
        print(f"ERROR: No versions found in generated CRD")
        return False
    
    # Find v1alpha1 version
    v1alpha1_version = None
    for version in generated_versions:
        if version.get('name') == 'v1alpha1':
            v1alpha1_version = version
            break
    
    if not v1alpha1_version:
        print(f"ERROR: v1alpha1 version not found in generated CRD")
        return False
    
    generated_schema = v1alpha1_version.get('schema', {}).get('openAPIV3Schema', {})
    
    # Read Helm CRD template as text to preserve Helm template syntax
    with open(helm_crd_path, 'r') as f:
        helm_crd_content = f.read()
    
    # Convert schema to YAML string with proper indentation
    # The schema section starts at column 12 (6 spaces for version item, 6 for schema)
    schema_yaml = yaml.dump(
        {'schema': {'openAPIV3Schema': generated_schema}},
        default_flow_style=False,
        sort_keys=False,
        width=120
    )
    
    # Indent the schema YAML to match the Helm template structure
    # Remove the first line (schema:) and indent the rest
    schema_lines = schema_yaml.split('\n')
    # Skip empty lines at start/end
    schema_lines = [line for line in schema_lines if line.strip() or len(schema_lines) - schema_lines.index(line) <= 2]
    
    # Find the schema section in the Helm CRD
    # Pattern: look for "schema:" followed by "openAPIV3Schema:" 
    # We need to replace everything from "schema:" to the next top-level key (served:, storage:, or subresources:)
    
    # Find the start of the schema section
    schema_start_pattern = r'(\s+)(schema:)\s*\n(\s+)(openAPIV3Schema:)'
    match = re.search(schema_start_pattern, helm_crd_content)
    
    if not match:
        print(f"ERROR: Could not find schema section in Helm CRD template")
        return False
    
    # Find the end of the schema section (next top-level key at same indentation level)
    start_pos = match.end()
    indent_level = len(match.group(1))  # Number of spaces for schema indentation
    
    # Look for the next key at the same or less indentation (but not deeper)
    # This will be "served:", "storage:", or "subresources:"
    end_pattern = r'\n(\s{' + str(indent_level) + r',})(served|storage|subresources):'
    end_match = re.search(end_pattern, helm_crd_content[start_pos:])
    
    if not end_match:
        # If no next key found, schema might be at the end of the version
        # Look for end of version (next version item or end of versions array)
        end_pattern = r'\n(\s{4,6})(- name:|served|storage|subresources):'
        end_match = re.search(end_pattern, helm_crd_content[start_pos:])
    
    if end_match:
        end_pos = start_pos + end_match.start()
    else:
        # Fallback: find end of file or next major section
        end_pos = len(helm_crd_content)
    
    # Extract the schema section to replace
    schema_section = helm_crd_content[match.start():end_pos]
    
    # Build replacement schema section
    # Preserve the original indentation
    indent = match.group(1)
    schema_indent = match.group(3)
    
    # Build the new schema section
    new_schema_section = f"{indent}schema:\n{schema_indent}openAPIV3Schema:\n"
    
    # Add the schema content with proper indentation
    # The openAPIV3Schema content needs to be indented by schema_indent + 2 spaces
    schema_content_indent = ' ' * (len(schema_indent) + 2)
    
    # Convert schema dict to YAML with proper indentation
    schema_content = yaml.dump(
        generated_schema,
        default_flow_style=False,
        sort_keys=False,
        width=120
    )
    
    # Indent each line of the schema content
    schema_content_lines = schema_content.split('\n')
    indented_schema_lines = []
    for line in schema_content_lines:
        if line.strip():  # Skip empty lines
            indented_schema_lines.append(schema_content_indent + line)
        else:
            indented_schema_lines.append('')
    
    new_schema_section += '\n'.join(indented_schema_lines)
    
    # Replace the schema section
    updated_content = (
        helm_crd_content[:match.start()] +
        new_schema_section +
        helm_crd_content[end_pos:]
    )
    
    # Write the updated content
    with open(helm_crd_path, 'w') as f:
        f.write(updated_content)
    
    print(f"✅ Successfully synced CRD schema from {generated_crd_path} to {helm_crd_path}")
    return True


def main():
    """Main function."""
    script_dir = Path(__file__).parent
    project_root = script_dir.parent
    
    generated_crd_path = project_root / 'config' / 'crd' / 'bases' / 'autoscaling.x-k8s.io_startupcpuboosts.yaml'
    helm_crd_path = project_root / 'charts' / 'kube-startup-cpu-boost' / 'templates' / 'startupcpuboost-crd.yaml'
    
    if not generated_crd_path.exists():
        print(f"ERROR: Generated CRD not found at {generated_crd_path}")
        print("Run 'make manifests' first to generate CRDs")
        sys.exit(1)
    
    if not helm_crd_path.exists():
        print(f"ERROR: Helm CRD template not found at {helm_crd_path}")
        sys.exit(1)
    
    if not sync_crd_schema(generated_crd_path, helm_crd_path):
        sys.exit(1)


if __name__ == '__main__':
    main()
