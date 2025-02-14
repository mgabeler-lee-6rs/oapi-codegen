package codegen

import (
	"errors"
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// MergeSchemas merges all the fields in the schemas supplied into one giant schema.
// The idea is that we merge all fields together into one schema.
func MergeSchemas(allOf openapi3.SchemaRefs, path []string) (Schema, error) {
	// If someone asked for the old way, for backward compatibility, return the
	// old style result.
	if globalState.options.Compatibility.OldMergeSchemas {
		return mergeSchemasV1(allOf, path)
	}

	merged, err := mergeSchemas(allOf)
	if err != nil {
		return Schema{}, err
	}
	return GenerateGoSchema(openapi3.NewSchemaRef("", merged), path)
}

func mergeSchemas(schemas openapi3.SchemaRefs) (*openapi3.Schema, error) {
	n := len(schemas)
	if n < 1 {
		return nil, errors.New("no schemas to merge")
	}

	var result *openapi3.Schema
	allOfs := make(openapi3.SchemaRefs, 0)
	oneOfs := make([]openapi3.SchemaRefs, 0)
	anyOfs := make([]openapi3.SchemaRefs, 0)

	// simple top-level merge
	for i := 0; i < n; i++ {
		curSchema, err := valueWithPropagatedRef(schemas[i])
		if err != nil {
			return nil, err
		}

		// extract allOf, oneOf, anyOf
		extractedAllOf, extractedOneOf, extractedAnyOf := extractSchemaCombiners(curSchema)

		// simple merge for now, these will be flattened in a second pass
		allOfs = append(allOfs, extractedAllOf...)

		// NOTE: oneOf and anyOf are different, see below
		if len(extractedOneOf) > 0 {
			oneOfs = append(oneOfs, extractedOneOf)
		}
		if len(extractedAnyOf) > 0 {
			anyOfs = append(anyOfs, extractedAnyOf)
		}

		// merge top-level fields
		result, err = mergeFields(result, *curSchema)
		if err != nil {
			return nil, fmt.Errorf("error merging schemas for AllOf: %w", err)
		}
	}

	// recursively flatten allOf schemas
	// We are going to make AllOf transitive, so that merging an AllOf that
	// contains AllOf's will result in a flat object.
	for _, schemaRef := range allOfs {
		var err error
		result, err = mergeSchemas(openapi3.SchemaRefs{
			openapi3.NewSchemaRef("", result),
			schemaRef,
		})
		if err != nil {
			return nil, fmt.Errorf("error flattening schemas for AllOf: %w", err)
		}
	}

	// recursively flatten properties
	for _, prop := range result.Properties {
		var err error
		prop.Value, err = mergeSchemas(openapi3.SchemaRefs{prop})
		if err != nil {
			return nil, fmt.Errorf("error flattening property schema: %w", err)
		}
	}

	// assemble oneOf schemas
	if len(oneOfs) > 0 {
		// recursively flatten the sets
		for _, oneOfSet := range oneOfs {
			for i := 0; i < len(oneOfSet); i++ {
				oneOfSchema := oneOfSet[i]
				flattenedOneOf, err := mergeSchemas(openapi3.SchemaRefs{oneOfSchema})
				if err != nil {
					return nil, fmt.Errorf("error flattening OneOf schema: %w", err)
				}
				oneOfSet[i] = openapi3.NewSchemaRef("", flattenedOneOf)
			}
		}
		// grab the first set of oneOfs for now, and error if there are more than 1
		if len(result.OneOf) > 0 || len(oneOfs) > 1 {
			// TODO: oneOf sets can't simply be merged, they likely need to
			//       be combined via allOf
			return nil, errors.New("multiple OneOf sets not supported")
		}
		result.OneOf = oneOfs[0]
	}

	// assemble anyOf schemas
	if len(anyOfs) > 0 {
		// recursively flatten the sets
		for _, anyOfSet := range anyOfs {
			for i := 0; i < len(anyOfSet); i++ {
				anyOfSchema := anyOfSet[i]
				flattenedAnyOf, err := mergeSchemas(openapi3.SchemaRefs{anyOfSchema})
				if err != nil {
					return nil, fmt.Errorf("error flattening AnyOf schema: %w", err)
				}
				anyOfSet[i] = openapi3.NewSchemaRef("", flattenedAnyOf)
			}
		}
		// grab the first set of anyOfs for now, and error if there are more than 1
		if len(result.AnyOf) > 0 || len(anyOfs) > 1 {
			// TODO: anyOf sets can't simply be merged, they likely need to
			//       be combined via allOf
			return nil, errors.New("multiple AnyOf sets not supported")
		}
		result.AnyOf = anyOfs[0]
	}

	return result, nil
}

// valueWithPropagatedRef returns a copy of ref schema with its Properties refs
// updated if ref itself is external. Otherwise, return ref.Value as-is.
func valueWithPropagatedRef(ref *openapi3.SchemaRef) (*openapi3.Schema, error) {
	if len(ref.Ref) == 0 || ref.Ref[0] == '#' {
		var schema openapi3.Schema = *ref.Value
		return &schema, nil
	}

	pathParts := strings.Split(ref.Ref, "#")
	if len(pathParts) < 1 || len(pathParts) > 2 {
		return nil, fmt.Errorf("unsupported reference: %s", ref.Ref)
	}
	remoteComponent := pathParts[0]

	// remote ref
	schema := *ref.Value
	for _, value := range schema.Properties {
		if len(value.Ref) > 0 && value.Ref[0] == '#' {
			// local reference, should propagate remote
			value.Ref = remoteComponent + value.Ref
		}
	}

	return &schema, nil
}

func extractSchemaCombiners(result *openapi3.Schema) (
	allOf openapi3.SchemaRefs,
	oneOf openapi3.SchemaRefs,
	anyOf openapi3.SchemaRefs,
) {
	var allOfResult openapi3.SchemaRefs
	var oneOfResult openapi3.SchemaRefs
	var anyOfResult openapi3.SchemaRefs

	if result != nil && result.AllOf != nil {
		allOfResult = result.AllOf
		result.AllOf = nil
	} else {
		allOfResult = make(openapi3.SchemaRefs, 0)
	}
	if result != nil && result.OneOf != nil {
		oneOfResult = result.OneOf
		result.OneOf = nil
	} else {
		oneOfResult = make(openapi3.SchemaRefs, 0)
	}
	if result != nil && result.AnyOf != nil {
		anyOfResult = result.AnyOf
		result.AnyOf = nil
	} else {
		anyOfResult = make(openapi3.SchemaRefs, 0)
	}

	return allOfResult, oneOfResult, anyOfResult
}

// FIXME: add comment
func mergeFields(result *openapi3.Schema, s2 openapi3.Schema) (*openapi3.Schema, error) {
	if result == nil {
		return &s2, nil
	}

	if s2.Extensions != nil {
		if result.Extensions == nil {
			result.Extensions = make(map[string]interface{})
		}
		for k, v := range s2.Extensions {
			// TODO: Check for collisions
			result.Extensions[k] = v
		}
	}

	if s2.Type.Slice() != nil {
		if result.Type.Slice() != nil && !equalTypes(result.Type, s2.Type) {
			return nil, fmt.Errorf("can not merge incompatible types: %v, %v", result.Type.Slice(), s2.Type.Slice())
		}
		result.Type = s2.Type
	}

	if result.Format != s2.Format {
		return nil, errors.New("can not merge incompatible formats")
	}

	// For Enums, do we union, or intersect? This is a bit vague. I choose
	// to be more permissive and union.
	result.Enum = append(result.Enum, s2.Enum...)

	// I don't know how to handle two different defaults.
	if s2.Default != nil {
		if result.Default != nil {
			return nil, errors.New("merging two sets of defaults is undefined")
		}
		result.Default = s2.Default
	}

	// We skip Example
	// We skip ExternalDocs

	// If two schemas disagree on any of these flags, we error out.
	if result.UniqueItems != s2.UniqueItems {
		return nil, errors.New("merging two schemas with different UniqueItems")
	}

	if result.ExclusiveMin != s2.ExclusiveMin {
		return nil, errors.New("merging two schemas with different ExclusiveMin")
	}

	if result.ExclusiveMax != s2.ExclusiveMax {
		return nil, errors.New("merging two schemas with different ExclusiveMax")
	}

	// for now, last one wins (this seems to be behavior at https://editor.swagger.io/)
	// TODO: what does the spec say
	if s2.Nullable != nil {
		result.Nullable = s2.Nullable
	}

	if result.ReadOnly != s2.ReadOnly {
		return nil, errors.New("merging two schemas with different ReadOnly")
	}

	if result.WriteOnly != s2.WriteOnly {
		return nil, errors.New("merging two schemas with different WriteOnly")
	}

	if result.AllowEmptyValue != s2.AllowEmptyValue {
		return nil, errors.New("merging two schemas with different AllowEmptyValue")
	}

	// Required. We merge these.
	result.Required = append(result.Required, s2.Required...)

	// We merge all properties
	mergedProps := make(map[string]*openapi3.SchemaRef)
	for k, v := range result.Properties {
		mergedProps[k] = v
	}
	for k, v := range s2.Properties {
		// TODO: detect conflicts
		mergedProps[k] = v
	}
	result.Properties = mergedProps

	if isAdditionalPropertiesExplicitFalse(result) || isAdditionalPropertiesExplicitFalse(&s2) {
		result.WithoutAdditionalProperties()
	} else if result.AdditionalProperties.Schema != nil {
		if s2.AdditionalProperties.Schema != nil {
			return nil, errors.New("merging two schemas with additional properties, this is unhandled")
		}
	} else {
		if s2.AdditionalProperties.Schema != nil {
			result.AdditionalProperties.Schema = s2.AdditionalProperties.Schema
		} else {
			if result.AdditionalProperties.Has != nil || s2.AdditionalProperties.Has != nil {
				result.WithAnyAdditionalProperties()
			}
		}
	}

	return result, nil
}

func equalTypes(t1 *openapi3.Types, t2 *openapi3.Types) bool {
	s1 := t1.Slice()
	s2 := t2.Slice()

	if len(s1) != len(s2) {
		return false
	}

	// NOTE that ideally we'd use `slices.Equal` but as we're currently supporting Go 1.20+, we can't use it (yet https://github.com/oapi-codegen/oapi-codegen/issues/1634)
	for i := range s1 {
		if s1[i] != s2[i] {
			return false
		}
	}

	return true
}
