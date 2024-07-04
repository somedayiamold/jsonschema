package jsonschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/maphash"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	PatchStrategyMerge   = "merge"
	PatchStrategyReplace = "replace"
)

// Merge validates and merge the given source and target, against the json-schema
//
// the source and target must be the raw json value. for number precision
// unmarshal with json.UseNumber().
//
// returns *ValidationError if merged source does not confirm with schema s.
// returns InfiniteLoopError if it detects loop during validation.
// returns InvalidJSONTypeError if it detects any non json value in source or target.
func (s *Schema) Merge(source, target any) (result any, err error) {
	return s.mergeValue(source, target, "")
}

func (s *Schema) mergeValue(source, target any, vloc string) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			switch r := r.(type) {
			case InfiniteLoopError, InvalidJSONTypeError:
				err = r.(error)
			default:
				panic(r)
			}
		}
	}()
	result, err = s.merge(nil, 0, "", source, target, vloc)
	if err != nil {
		ve := ValidationError{
			KeywordLocation:         "",
			AbsoluteKeywordLocation: s.Location,
			InstanceLocation:        vloc,
			Message:                 fmt.Sprintf("doesn't validate with %s", s.Location),
		}
		return nil, ve.causes(err)
	}
	return result, nil
}

// merge validates and merge data with this schema.
func (s *Schema) merge(scope []schemaRef, vscope int, spath string, source, target any, vloc string) (data any, err error) {
	var result validationResult
	validationError := func(keywordPath string, format string, a ...any) *ValidationError {
		return &ValidationError{
			KeywordLocation:         keywordLocation(scope, keywordPath),
			AbsoluteKeywordLocation: joinPtr(s.Location, keywordPath),
			InstanceLocation:        vloc,
			Message:                 fmt.Sprintf(format, a...),
		}
	}

	sref := schemaRef{spath, s, false}
	// log.V2.Debug().Str("source:").Obj(source).Str("target:").Obj(target).Emit()
	if err := checkLoop(scope[len(scope)-vscope:], sref); err != nil {
		panic(err)
	}
	scope = append(scope, sref)
	vscope++

	// populate result
	switch src := source.(type) {
	case map[string]any:
		result.unevalProps = make(map[string]struct{})
		for pname := range src {
			result.unevalProps[pname] = struct{}{}
		}
	case []any:
		result.unevalItems = make(map[int]struct{})
		for i := range src {
			result.unevalItems[i] = struct{}{}
		}
	}

	validate := func(sch *Schema, schPath string, source, target any, vpath string) (any, error) {
		vloc := vloc
		if vpath != "" {
			vloc += "/" + vpath
		}
		return sch.merge(scope, 0, schPath, source, target, vloc)
	}

	validateInplace := func(sch *Schema, schPath string) error {
		vr, err := sch.validate(scope, vscope, schPath, source, vloc)
		if err == nil {
			// update result
			for pname := range result.unevalProps {
				if _, ok := vr.unevalProps[pname]; !ok {
					delete(result.unevalProps, pname)
				}
			}
			for i := range result.unevalItems {
				if _, ok := vr.unevalItems[i]; !ok {
					delete(result.unevalItems, i)
				}
			}
		}
		return err
	}

	if s.Always != nil {
		if !*s.Always {
			return result, validationError("", "not allowed")
		}
		return result, nil
	}

	if len(s.Types) > 0 {
		vType := jsonType(source)
		matched := false
		for _, t := range s.Types {
			if vType == t {
				matched = true
				break
			} else if t == "integer" && vType == "number" {
				num, _ := new(big.Rat).SetString(fmt.Sprint(source))
				if num.IsInt() {
					matched = true
					break
				}
			}
		}
		if !matched {
			return result, validationError("type", "expected %s, but got %s", strings.Join(s.Types, " or "), vType)
		}
	}

	var errors []error

	if len(s.Constant) > 0 {
		if !equals(source, s.Constant[0]) {
			switch jsonType(s.Constant[0]) {
			case "object", "array":
				errors = append(errors, validationError("const", "const failed"))
			default:
				errors = append(errors, validationError("const", "value must be %#v", s.Constant[0]))
			}
		}
	}

	if len(s.Enum) > 0 {
		matched := false
		for _, item := range s.Enum {
			if equals(source, item) {
				matched = true
				break
			}
		}
		if !matched {
			errors = append(errors, validationError("enum", "%s", s.enumError))
		}
	}

	if s.format != nil && !s.format(source) {
		val := source
		if v, ok := source.(string); ok {
			val = quote(v)
		}
		errors = append(errors, validationError("format", "%v is not valid %s", val, quote(s.Format)))
	}

	// 删除字段
	switch targetType := target.(type) {
	case map[string]any:
		for k, v := range targetType {
			if v == nil {
				// log.V2.Debug().Str("remove: ", k, " from target").Emit()
				delete(targetType, k)
				if sourceType, ok := source.(map[string]any); ok {
					// log.V2.Debug().Str("remove: ", k, " from source").Emit()
					delete(sourceType, k)
				}
			}
		}
	}

	switch sourceType := source.(type) {
	case map[string]any:
		var targetType map[string]any
		if target != nil {
			var ok bool
			targetType, ok = target.(map[string]any)
			if !ok {
				return result, validationError("format", "target is not map[string]interface{}")
			}
		}
		if s.MinProperties != -1 && len(sourceType) < s.MinProperties {
			errors = append(errors, validationError("minProperties", "minimum %d properties allowed, but found %d properties", s.MinProperties, len(sourceType)))
		}
		if s.MaxProperties != -1 && len(sourceType) > s.MaxProperties {
			errors = append(errors, validationError("maxProperties", "maximum %d properties allowed, but found %d properties", s.MaxProperties, len(sourceType)))
		}
		if len(s.Required) > 0 {
			var missing []string
			for _, pname := range s.Required {
				if _, ok := sourceType[pname]; !ok {
					missing = append(missing, quote(pname))
				}
				if val, ok := targetType[pname]; ok && val == nil {
					missing = append(missing, quote(pname))
				}
			}
			if len(missing) > 0 {
				errors = append(errors, validationError("required", "missing properties: %s", strings.Join(missing, ", ")))
			}
		}

		for pname, sch := range s.Properties {
			// log.V2.Debug().Str("pname: ", pname, ", sch:").Obj(sch).Emit()
			if sourceVal, ok := sourceType[pname]; ok {
				// log.V2.Debug().Str("sourceVal:").Obj(sourceVal).Emit()
				delete(result.unevalProps, pname)
				// if targetVal, ok := targetType[pname]; ok {
				// 	log.V2.Debug().Str("targetVal:").Obj(targetVal).Emit()
				// 	// sourceVal是数组的话，交给下一层级进行合并策略处理
				// 	if _, ok := sourceVal.([]any); !ok {
				// 		sourceType[pname] = targetVal
				// 	}
				// }
				targetVal := targetType[pname]
				// log.V2.Debug().Str("sourceVal:").Obj(targetVal).Emit()
				res, err := validate(sch, "properties/"+escape(pname), sourceVal, targetVal, escape(pname))
				if err != nil {
					errors = append(errors, err)
				} else {
					sourceType[pname] = res
				}
				delete(targetType, pname)
			}
		}
		// 不在必需字段中的其他字段需要merge到source中
		for k, v := range targetType {
			// log.V2.Debug().Str("key: ", k, ", value:").Obj(v).Emit()
			sourceType[k] = v
		}

		if s.PropertyNames != nil {
			for pname, pvalue := range sourceType {
				if res, err := validate(s.PropertyNames, "propertyNames", pvalue, targetType[pname], escape(pname)); err != nil {
					errors = append(errors, err)
				} else {
					sourceType[pname] = res
				}
			}
		}

		if s.RegexProperties {
			for pname := range sourceType {
				if !s.regexPropertiesFormat(pname) {
					errors = append(errors, validationError("", "patternProperty %s is not valid regex", quote(pname)))
				}
			}
		}
		for pattern, sch := range s.PatternProperties {
			for pname, pvalue := range sourceType {
				if pattern.MatchString(pname) {
					delete(result.unevalProps, pname)
					if res, err := validate(sch, "patternProperties/"+escape(pattern.String()), pvalue, targetType[pname], escape(pname)); err != nil {
						errors = append(errors, err)
					} else {
						sourceType[pname] = res
					}
				}
			}
		}
		if s.AdditionalProperties != nil {
			if allowed, ok := s.AdditionalProperties.(bool); ok {
				if !allowed && len(result.unevalProps) > 0 {
					errors = append(errors, validationError("additionalProperties", "additionalProperties %s not allowed", result.unevalPnames()))
				}
			} else {
				schema := s.AdditionalProperties.(*Schema)
				for pname := range result.unevalProps {
					if pvalue, ok := sourceType[pname]; ok {
						if res, err := validate(schema, "additionalProperties", pvalue, targetType[pname], escape(pname)); err != nil {
							errors = append(errors, err)
						} else {
							sourceType[pname] = res
						}
					}
				}
			}
			result.unevalProps = nil
		}
		for dname, dvalue := range s.Dependencies {
			if _, ok := sourceType[dname]; ok {
				switch dvalue := dvalue.(type) {
				case *Schema:
					if err := validateInplace(dvalue, "dependencies/"+escape(dname)); err != nil {
						errors = append(errors, err)
					}
				case []string:
					for i, pname := range dvalue {
						if _, ok := sourceType[pname]; !ok {
							errors = append(errors, validationError("dependencies/"+escape(dname)+"/"+strconv.Itoa(i), "property %s is required, if %s property exists", quote(pname), quote(dname)))
						}
					}
				}
			}
		}
		for dname, dvalue := range s.DependentRequired {
			if _, ok := sourceType[dname]; ok {
				for i, pname := range dvalue {
					if _, ok := sourceType[pname]; !ok {
						errors = append(errors, validationError("dependentRequired/"+escape(dname)+"/"+strconv.Itoa(i), "property %s is required, if %s property exists", quote(pname), quote(dname)))
					}
				}
			}
		}
		for dname, sch := range s.DependentSchemas {
			if _, ok := sourceType[dname]; ok {
				if err := validateInplace(sch, "dependentSchemas/"+escape(dname)); err != nil {
					errors = append(errors, err)
				}
			}
		}

	case []any:
		// log.V2.Debug().Str("array found:").Obj(s).Str("source:").Obj(sourceType).Str("target:").Obj(target).Emit()
		// source和targe值均为array类型，否则类型不匹配，需要报错
		var targetMap map[string]any
		if target != nil {
			targetType, ok := target.([]any)
			if !ok {
				return result, validationError("format", "target is not []interface{}")
			}
			// 创建一个map，用于存放唯一key对应的原始值
			sourceMap := make(map[string]any, len(sourceType))
			// mergeMap := make(map[string]any, len(sourceType)+len(targetType))
			for _, item := range sourceType {
				tmp := item
				var uniqueKey string
				itemObj, ok := tmp.(map[string]any)
				if ok && len(s.XPatchMergeKeys) > 0 {
					for _, key := range s.XPatchMergeKeys {
						uniqueKey = fmt.Sprintf("%s:%s", uniqueKey, itemObj[key])
					}
				} else {
					uniqueKey = fmt.Sprintf("%s", tmp)
				}
				// log.V2.Debug().Str("uniqueKey: ", uniqueKey, ", item:").Obj(tmp).Emit()
				sourceMap[uniqueKey] = tmp
			}
			targetMap = make(map[string]any, len(targetType))
			for _, item := range targetType {
				tmp := item
				var uniqueKey string
				itemObj, ok := tmp.(map[string]any)
				if ok && len(s.XPatchMergeKeys) > 0 {
					for _, key := range s.XPatchMergeKeys {
						uniqueKey = fmt.Sprintf("%s:%s", uniqueKey, itemObj[key])
					}
					// 如果source中不存在唯一键的元素，如果存在相同唯一键的数据，则要继续进行合并
					if _, ok := sourceMap[uniqueKey]; !ok {
						// log.V2.Debug().Str("source map uniqueKey: ", uniqueKey, ", item:").Obj(itemObj).Emit()
						sourceMap[uniqueKey] = itemObj
					} else {
						// log.V2.Debug().Str("target map uniqueKey: ", uniqueKey, ", item:").Obj(itemObj).Emit()
						targetMap[uniqueKey] = itemObj
					}
				} else {
					// 如果元数类型不是map[string]interface{}，则直接合并
					uniqueKey = fmt.Sprintf("%s", tmp)
					sourceMap[uniqueKey] = tmp
					// log.V2.Debug().Str("uniqueKey: ", uniqueKey, ", item:").Obj(tmp).Emit()
				}
			}
			result := make([]any, 0, len(sourceMap))
			for _, item := range sourceMap {
				result = append(result, item)
			}
			sourceType = result
			source = result
			// target = target
		}

		if s.MinItems != -1 && len(sourceType) < s.MinItems {
			errors = append(errors, validationError("minItems", "minimum %d items required, but found %d items", s.MinItems, len(sourceType)))
		}
		if s.MaxItems != -1 && len(sourceType) > s.MaxItems {
			errors = append(errors, validationError("maxItems", "maximum %d items required, but found %d items", s.MaxItems, len(sourceType)))
		}
		if s.UniqueItems {
			if len(sourceType) <= 20 {
			outer1:
				for i := 1; i < len(sourceType); i++ {
					for j := 0; j < i; j++ {
						if equals(sourceType[i], sourceType[j]) {
							errors = append(errors, validationError("uniqueItems", "items at index %d and %d are equal", j, i))
							break outer1
						}
					}
				}
			} else {
				m := make(map[uint64][]int)
				var h maphash.Hash
			outer2:
				for i, item := range sourceType {
					h.Reset()
					hash(item, &h)
					k := h.Sum64()
					arr, ok := m[k]
					if ok {
						for _, j := range arr {
							if equals(sourceType[j], item) {
								errors = append(errors, validationError("uniqueItems", "items at index %d and %d are equal", j, i))
								break outer2
							}
						}
					}
					arr = append(arr, i)
					m[k] = arr
				}
			}
		}

		// items + additionalItems
		switch items := s.Items.(type) {
		case *Schema:
			for i, item := range sourceType {
				if _, err := validate(items, "items", item, target, strconv.Itoa(i)); err != nil {
					errors = append(errors, err)
				}
			}
			result.unevalItems = nil
		case []*Schema:
			for i, item := range sourceType {
				if i < len(items) {
					delete(result.unevalItems, i)
					if _, err := validate(items[i], "items/"+strconv.Itoa(i), item, target, strconv.Itoa(i)); err != nil {
						errors = append(errors, err)
					}
				} else if sch, ok := s.AdditionalItems.(*Schema); ok {
					delete(result.unevalItems, i)
					if _, err := validate(sch, "additionalItems", item, target, strconv.Itoa(i)); err != nil {
						errors = append(errors, err)
					}
				} else {
					break
				}
			}
			if additionalItems, ok := s.AdditionalItems.(bool); ok {
				if additionalItems {
					result.unevalItems = nil
				} else if len(sourceType) > len(items) {
					errors = append(errors, validationError("additionalItems", "only %d items are allowed, but found %d items", len(items), len(sourceType)))
				}
			}
		}

		// prefixItems + items
		for i, item := range sourceType {
			// log.V2.Debug().Str("i:").Int(i).Str("item:").Obj(item).Emit()
			if i < len(s.PrefixItems) {
				delete(result.unevalItems, i)
				if _, err := validate(s.PrefixItems[i], "prefixItems/"+strconv.Itoa(i), item, target, strconv.Itoa(i)); err != nil {
					errors = append(errors, err)
				}
			} else if s.Items2020 != nil {
				delete(result.unevalItems, i)
				if itemObj, ok := item.(map[string]any); ok {
					var uniqueKey string
					for _, key := range s.XPatchMergeKeys {
						uniqueKey = fmt.Sprintf("%s:%s", uniqueKey, itemObj[key])
					}
					target = targetMap[uniqueKey]
				}
				if _, err := validate(s.Items2020, "items", item, target, strconv.Itoa(i)); err != nil {
					errors = append(errors, err)
				}
			} else {
				break
			}
		}

		// contains + minContains + maxContains
		if s.Contains != nil && (s.MinContains != -1 || s.MaxContains != -1) {
			matched := 0
			var causes []error
			for i, item := range sourceType {
				if _, err := validate(s.Contains, "contains", item, target, strconv.Itoa(i)); err != nil {
					causes = append(causes, err)
				} else {
					matched++
					if s.ContainsEval {
						delete(result.unevalItems, i)
					}
				}
			}
			if s.MinContains != -1 && matched < s.MinContains {
				errors = append(errors, validationError("minContains", "valid must be >= %d, but got %d", s.MinContains, matched).add(causes...))
			}
			if s.MaxContains != -1 && matched > s.MaxContains {
				errors = append(errors, validationError("maxContains", "valid must be <= %d, but got %d", s.MaxContains, matched))
			}
		}

	case string:
		if target != nil {
			source = target
		}
		// minLength + maxLength
		if s.MinLength != -1 || s.MaxLength != -1 {
			length := utf8.RuneCount([]byte(sourceType))
			if s.MinLength != -1 && length < s.MinLength {
				errors = append(errors, validationError("minLength", "length must be >= %d, but got %d", s.MinLength, length))
			}
			if s.MaxLength != -1 && length > s.MaxLength {
				errors = append(errors, validationError("maxLength", "length must be <= %d, but got %d", s.MaxLength, length))
			}
		}

		if s.Pattern != nil && !s.Pattern.MatchString(sourceType) {
			errors = append(errors, validationError("pattern", "does not match pattern %s", quote(s.Pattern.String())))
		}

		// contentEncoding + contentMediaType
		if s.decoder != nil || s.mediaType != nil {
			decoded := s.ContentEncoding == ""
			var content []byte
			if s.decoder != nil {
				b, err := s.decoder(sourceType)
				if err != nil {
					errors = append(errors, validationError("contentEncoding", "value is not %s encoded", s.ContentEncoding))
				} else {
					content, decoded = b, true
				}
			}
			if decoded && s.mediaType != nil {
				if s.decoder == nil {
					content = []byte(sourceType)
				}
				if err := s.mediaType(content); err != nil {
					errors = append(errors, validationError("contentMediaType", "value is not of mediatype %s", quote(s.ContentMediaType)))
				}
			}
			if decoded && s.ContentSchema != nil {
				contentJSON, err := unmarshal(bytes.NewReader(content))
				if err != nil {
					errors = append(errors, validationError("contentSchema", "value is not valid json"))
				} else {
					_, err := validate(s.ContentSchema, "contentSchema", contentJSON, target, "")
					if err != nil {
						errors = append(errors, err)
					}
				}
			}
		}

	case json.Number, float32, float64, int, int8, int32, int64, uint, uint8, uint32, uint64:
		if target != nil {
			source = target
		}
		// lazy convert to *big.Rat to avoid allocation
		var numVal *big.Rat
		num := func() *big.Rat {
			if numVal == nil {
				numVal, _ = new(big.Rat).SetString(fmt.Sprint(sourceType))
			}
			return numVal
		}
		f64 := func(r *big.Rat) float64 {
			f, _ := r.Float64()
			return f
		}
		if s.Minimum != nil && num().Cmp(s.Minimum) < 0 {
			errors = append(errors, validationError("minimum", "must be >= %v but found %v", f64(s.Minimum), sourceType))
		}
		if s.ExclusiveMinimum != nil && num().Cmp(s.ExclusiveMinimum) <= 0 {
			errors = append(errors, validationError("exclusiveMinimum", "must be > %v but found %v", f64(s.ExclusiveMinimum), sourceType))
		}
		if s.Maximum != nil && num().Cmp(s.Maximum) > 0 {
			errors = append(errors, validationError("maximum", "must be <= %v but found %v", f64(s.Maximum), sourceType))
		}
		if s.ExclusiveMaximum != nil && num().Cmp(s.ExclusiveMaximum) >= 0 {
			errors = append(errors, validationError("exclusiveMaximum", "must be < %v but found %v", f64(s.ExclusiveMaximum), sourceType))
		}
		if s.MultipleOf != nil {
			if q := new(big.Rat).Quo(num(), s.MultipleOf); !q.IsInt() {
				errors = append(errors, validationError("multipleOf", "%v not multipleOf %v", sourceType, f64(s.MultipleOf)))
			}
		}
	}

	// $ref + $recursiveRef + $dynamicRef
	validateRef := func(sch *Schema, refPath string) error {
		if sch != nil {
			if err := validateInplace(sch, refPath); err != nil {
				url := sch.Location
				if s.url() == sch.url() {
					url = sch.loc()
				}
				return validationError(refPath, "doesn't validate with %s", quote(url)).causes(err)
			}
		}
		return nil
	}
	if err := validateRef(s.Ref, "$ref"); err != nil {
		errors = append(errors, err)
	}
	if s.RecursiveRef != nil {
		sch := s.RecursiveRef
		if sch.RecursiveAnchor {
			// recursiveRef based on scope
			for _, e := range scope {
				if e.schema.RecursiveAnchor {
					sch = e.schema
					break
				}
			}
		}
		if err := validateRef(sch, "$recursiveRef"); err != nil {
			errors = append(errors, err)
		}
	}
	if s.DynamicRef != nil {
		sch := s.DynamicRef
		if s.dynamicRefAnchor != "" && sch.DynamicAnchor == s.dynamicRefAnchor {
			// dynamicRef based on scope
			for i := len(scope) - 1; i >= 0; i-- {
				sr := scope[i]
				if sr.discard {
					break
				}
				for _, da := range sr.schema.dynamicAnchors {
					if da.DynamicAnchor == s.DynamicRef.DynamicAnchor && da != s.DynamicRef {
						sch = da
						break
					}
				}
			}
		}
		if err := validateRef(sch, "$dynamicRef"); err != nil {
			errors = append(errors, err)
		}
	}

	if s.Not != nil && validateInplace(s.Not, "not") == nil {
		errors = append(errors, validationError("not", "not failed"))
	}

	for i, sch := range s.AllOf {
		schPath := "allOf/" + strconv.Itoa(i)
		if err := validateInplace(sch, schPath); err != nil {
			errors = append(errors, validationError(schPath, "allOf failed").add(err))
		}
	}

	if len(s.AnyOf) > 0 {
		matched := false
		var causes []error
		for i, sch := range s.AnyOf {
			if err := validateInplace(sch, "anyOf/"+strconv.Itoa(i)); err == nil {
				matched = true
			} else {
				causes = append(causes, err)
			}
		}
		if !matched {
			errors = append(errors, validationError("anyOf", "anyOf failed").add(causes...))
		}
	}

	if len(s.OneOf) > 0 {
		matched := -1
		var causes []error
		for i, sch := range s.OneOf {
			if err := validateInplace(sch, "oneOf/"+strconv.Itoa(i)); err == nil {
				if matched == -1 {
					matched = i
				} else {
					errors = append(errors, validationError("oneOf", "valid against schemas at indexes %d and %d", matched, i))
					break
				}
			} else {
				causes = append(causes, err)
			}
		}
		if matched == -1 {
			errors = append(errors, validationError("oneOf", "oneOf failed").add(causes...))
		}
	}

	// if + then + else
	if s.If != nil {
		err := validateInplace(s.If, "if")
		// "if" leaves dynamic scope
		scope[len(scope)-1].discard = true
		if err == nil {
			if s.Then != nil {
				if err := validateInplace(s.Then, "then"); err != nil {
					errors = append(errors, validationError("then", "if-then failed").add(err))
				}
			}
		} else {
			if s.Else != nil {
				if err := validateInplace(s.Else, "else"); err != nil {
					errors = append(errors, validationError("else", "if-else failed").add(err))
				}
			}
		}
		// restore dynamic scope
		scope[len(scope)-1].discard = false
	}

	// for _, ext := range s.Extensions {
	// 	if err := ext.Validate(ValidationContext{result, validate, validateInplace, validationError}, source, target); err != nil {
	// 		errors = append(errors, err)
	// 	}
	// }

	// unevaluatedProperties + unevaluatedItems
	switch v := source.(type) {
	case map[string]any:
		if s.UnevaluatedProperties != nil {
			for pname := range result.unevalProps {
				if pvalue, ok := v[pname]; ok {
					if _, err := validate(s.UnevaluatedProperties, "unevaluatedProperties", pvalue, target, escape(pname)); err != nil {
						errors = append(errors, err)
					}
				}
			}
			result.unevalProps = nil
		}
	case []any:
		if s.UnevaluatedItems != nil {
			for i := range result.unevalItems {
				if _, err := validate(s.UnevaluatedItems, "unevaluatedItems", v[i], target, strconv.Itoa(i)); err != nil {
					errors = append(errors, err)
				}
			}
			result.unevalItems = nil
		}
	}

	switch len(errors) {
	case 0:
		// log.V2.Debug().Str("return source:").Obj(source).Emit()
		return source, nil
	case 1:
		return source, errors[0]
	default:
		return source, validationError("", "").add(errors...) // empty message, used just for wrapping
	}
}
