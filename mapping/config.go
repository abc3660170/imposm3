package mapping

import (
	"errors"
	"fmt"
	"io/ioutil"
	"regexp"

	"github.com/omniscale/imposm3/element"

	"gopkg.in/yaml.v2"
)

type Field struct {
	Name       string                 `yaml:"name"`
	Key        Key                    `yaml:"key"`
	Keys       []Key                  `yaml:"keys"`
	Type       string                 `yaml:"type"`
	Args       map[string]interface{} `yaml:"args"`
	FromMember bool                   `yaml:"from_member"`
}

type Table struct {
	Name         string
	Type         TableType             `yaml:"type"`
	Mapping      KeyValues             `yaml:"mapping"`
	Mappings     map[string]SubMapping `yaml:"mappings"`
	TypeMappings TypeMappings          `yaml:"type_mappings"`
	Fields       []*Field              `yaml:"columns"` // TODO rename Fields internaly to Columns
	OldFields    []*Field              `yaml:"fields"`
	Filters      *Filters              `yaml:"filters"`
}

type GeneralizedTable struct {
	Name            string
	SourceTableName string  `yaml:"source"`
	Tolerance       float64 `yaml:"tolerance"`
	SqlFilter       string  `yaml:"sql_filter"`
}

type Filters struct {
	ExcludeTags              *[][]string `yaml:"exclude_tags"`
	ExcludeNegatedTags       *[][]string `yaml:"exclude_negated_tags"`
	ExcludeRegexpTags        *[][]string `yaml:"exclude_regexp_tags"`
	ExcludeNegatedRegexpTags *[][]string `yaml:"exclude_negated_regexp_tags"`
}

type Tables map[string]*Table

type GeneralizedTables map[string]*GeneralizedTable

type Mapping struct {
	Tables            Tables            `yaml:"tables"`
	GeneralizedTables GeneralizedTables `yaml:"generalized_tables"`
	Tags              Tags              `yaml:"tags"`
	// SingleIdSpace mangles the overlapping node/way/relation IDs
	// to be unique (nodes positive, ways negative, relations negative -1e17)
	SingleIdSpace bool `yaml:"use_single_id_space"`
}

type Tags struct {
	LoadAll bool  `yaml:"load_all"`
	Exclude []Key `yaml:"exclude"`
}

type orderedValue struct {
	value Value
	order int
}
type KeyValues map[Key][]orderedValue

func (kv *KeyValues) UnmarshalYAML(unmarshal func(interface{}) error) error {
	if *kv == nil {
		*kv = make(map[Key][]orderedValue)
	}
	slice := yaml.MapSlice{}
	err := unmarshal(&slice)
	if err != nil {
		return err
	}
	order := 0
	for _, item := range slice {
		k, ok := item.Key.(string)
		if !ok {
			return fmt.Errorf("mapping key '%s' not a string", k)
		}
		values, ok := item.Value.([]interface{})
		if !ok {
			return fmt.Errorf("mapping key '%s' not a string", k)
		}
		for _, v := range values {
			if v, ok := v.(string); ok {
				(*kv)[Key(k)] = append((*kv)[Key(k)], orderedValue{value: Value(v), order: order})
			} else {
				return fmt.Errorf("mapping value '%s' not a string", v)
			}
			order += 1
		}
	}
	return nil
}

type SubMapping struct {
	Mapping KeyValues
}

type TypeMappings struct {
	Points      KeyValues `yaml:"points"`
	LineStrings KeyValues `yaml:"linestrings"`
	Polygons    KeyValues `yaml:"polygons"`
}

type ElementFilter func(tags *element.Tags) bool

type TagTables map[Key]map[Value][]OrderedDestTable

type DestTable struct {
	Name       string
	SubMapping string
}

type OrderedDestTable struct {
	DestTable
	order int
}

type TableType string

func (tt *TableType) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "":
		return errors.New("missing table type")
	case `"point"`:
		*tt = PointTable
	case `"linestring"`:
		*tt = LineStringTable
	case `"polygon"`:
		*tt = PolygonTable
	case `"geometry"`:
		*tt = GeometryTable
	case `"relation"`:
		*tt = RelationTable
	case `"relation_member"`:
		*tt = RelationMemberTable
	default:
		return errors.New("unknown type " + string(data))
	}
	return nil
}

const (
	PolygonTable        TableType = "polygon"
	LineStringTable     TableType = "linestring"
	PointTable          TableType = "point"
	GeometryTable       TableType = "geometry"
	RelationTable       TableType = "relation"
	RelationMemberTable TableType = "relation_member"
)

func NewMapping(filename string) (*Mapping, error) {
	f, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	mapping := Mapping{}
	err = yaml.Unmarshal(f, &mapping)
	if err != nil {
		return nil, err
	}

	err = mapping.prepare()
	if err != nil {
		return nil, err
	}
	return &mapping, nil
}

func (t *Table) ExtraTags() map[Key]bool {
	tags := make(map[Key]bool)
	for _, field := range t.Fields {
		if field.Key != "" {
			tags[field.Key] = true
		}
		for _, k := range field.Keys {
			tags[k] = true
		}
	}
	return tags
}

func (m *Mapping) prepare() error {
	for name, t := range m.Tables {
		t.Name = name
		if t.OldFields != nil {
			// todo deprecate 'fields'
			t.Fields = t.OldFields
		}
	}

	for name, t := range m.GeneralizedTables {
		t.Name = name
	}
	return nil
}

func (tt TagTables) addFromMapping(mapping KeyValues, table DestTable) {
	for key, vals := range mapping {
		for _, v := range vals {
			vals, ok := tt[key]
			tbl := OrderedDestTable{DestTable: table, order: v.order}
			if ok {
				vals[v.value] = append(vals[v.value], tbl)
			} else {
				tt[key] = make(map[Value][]OrderedDestTable)
				tt[key][v.value] = append(tt[key][v.value], tbl)
			}
		}
	}
}

func (m *Mapping) mappings(tableType TableType, mappings TagTables) {
	for name, t := range m.Tables {
		if t.Type != GeometryTable && t.Type != tableType {
			continue
		}
		mappings.addFromMapping(t.Mapping, DestTable{Name: name})

		for subMappingName, subMapping := range t.Mappings {
			mappings.addFromMapping(subMapping.Mapping, DestTable{Name: name, SubMapping: subMappingName})
		}

		switch tableType {
		case PointTable:
			mappings.addFromMapping(t.TypeMappings.Points, DestTable{Name: name})
		case LineStringTable:
			mappings.addFromMapping(t.TypeMappings.LineStrings, DestTable{Name: name})
		case PolygonTable:
			mappings.addFromMapping(t.TypeMappings.Polygons, DestTable{Name: name})
		}
	}
}

func (m *Mapping) tables(tableType TableType) map[string]*TableFields {
	result := make(map[string]*TableFields)
	for name, t := range m.Tables {
		if t.Type == tableType || t.Type == "geometry" {
			result[name] = t.TableFields()
		}
	}
	return result
}

func (m *Mapping) extraTags(tableType TableType, tags map[Key]bool) {
	for _, t := range m.Tables {
		if t.Type != tableType && t.Type != "geometry" {
			continue
		}
		for key, _ := range t.ExtraTags() {
			tags[key] = true
		}
		if t.Filters != nil && t.Filters.ExcludeTags != nil {
			for _, keyVal := range *t.Filters.ExcludeTags {
				tags[Key(keyVal[0])] = true
			}
		}
	}
}

func makeElementFiltersFunction(virtualTrue bool, virtualFalse bool, filterType string, filterKey, filterValue string) func(tags *element.Tags) bool {
	//  if ExcludeTags :  virtualTrue == true
	//  if ExcludeNegatedTags :  virtualTrue == false
	return func(tags *element.Tags) bool {
		if v, ok := (*tags)[filterKey]; ok {
			if filterValue == "__any__" || v == filterValue {
				return virtualFalse
			}
		} else if filterValue == "__nil__" {
			return virtualFalse
		}
		return virtualTrue
	}
}

func makeElementFiltersListFunction(virtualTrue bool, virtualFalse bool, filterType string, filter []string) func(tags *element.Tags) bool {
	//  if ExcludeTags :  virtualTrue == true
	//  if ExcludeNegatedTags :  virtualTrue == false
	filterKey := filter[0]
	filterArray := filter[1:]

	for _, filterValue := range filterArray {
		if filterValue == "__nil__" || filterValue == "__any__" {
			log.Errorf("mapping filter error: %s  key:%s  + Array filtering ( more than 1 value ) with `__nil__`  or `__any__` value not allowed!", filterType, filterKey)
		}
	}

	return func(tags *element.Tags) bool {
		if v, ok := (*tags)[filterKey]; ok {
			for _, filterValue := range filterArray {
				if v == filterValue {
					return virtualFalse
				}
			}
		}
		return virtualTrue
	}
}

func makeElementRegexpFiltersFunction(virtualTrue bool, virtualFalse bool, filterType string, filterKey, regexprValue string) func(tags *element.Tags) bool {

	//  if ExcludeRegexpTags        :  virtualTrue == true
	//  if ExcludeNegatedRegexpTags :  virtualTrue == false

	// Compile regular expression
	// if not valid regexp --> panic !
	r := regexp.MustCompile(regexprValue)

	return func(tags *element.Tags) bool {
		if v, ok := (*tags)[filterKey]; ok {
			if r.MatchString(v) {
				return virtualFalse
			}
		}
		return virtualTrue
	}
}

/*
# Advanced filtering syntax:
#
#   exclude_tags
#   - [ key, val]                                  // AND key != val
#   - [ key, __nil__]                              // AND key IS NOT NULL
#   - [ key, __any__]                              // AND key IS NULL
#   - [ key, val1,val2]                            // AND key not in ( val1,val2 )                          // check: __nil__,__any__  not allowed
#   - [ key, val1,val2,val3, ... valn]             // AND key not in ( val1,val2,val3, ... valn)            // check: __nil__,__any__  not allowed
#   exclude_negated_tags
#   - [ key, val]                                  // AND key = val
#   - [ key, __nil__]                              // AND key IS NULL
#   - [ key, __any__]                              // AND key IS NOT NULL
#   - [ key, val1,val2]                            // AND key in ( val1, val2 )                              // check: __nil__,__any__  not allowed
#   - [ key, val1,val2,val3, ... valn]             // AND key in ( val1,val2,val3, ... valn)                 // check: __nil__,__any__  not allowed
#   exclude_regexp_tags:
#   - [ key, regexpr]                              // AND NOT ( regexpr.MatchString ( key.value) == true )       // see https://golang.org/pkg/regexp/
#   exclude_negated_regexp_tags:
#   - [ key, regexpr]                              // AND ( regexpr.MatchString ( key.value) == true )   // see https://golang.org/pkg/regexp/
#
# Internal processing order:  exclude_tags ->  exclude_negated_tags -> exclude_regexp_tags -> exclude_negated_regexp_tags
#
# if you want test Regexp -> https://regex-golang.appspot.com/assets/html/index.html
#
# see test examples : config_test.go and config_test_mapping.yml
# -------------------------------------------------------------------------------------------------------------------------------------------------
*/

func (m *Mapping) ElementFilters() map[string][]ElementFilter {
	result := make(map[string][]ElementFilter)
	for name, t := range m.Tables {
		if t.Filters == nil {
			continue
		}

		// exclude_tags
		if t.Filters.ExcludeTags != nil {
			for _, filterKeyVal := range *t.Filters.ExcludeTags {
				if len(filterKeyVal) == 2 {
					result[name] = append(result[name], makeElementFiltersFunction(true, false, "exclude_tags", filterKeyVal[0], filterKeyVal[1]))
				} else if len(filterKeyVal) > 2 {
					result[name] = append(result[name], makeElementFiltersListFunction(true, false, "exclude_tags", filterKeyVal))
				} else if len(filterKeyVal) < 2 {
					log.Errorf("mapping filter error: %s  key:%s  need at least 1 more value !", "exclude_tags", filterKeyVal[0])
				}
			}
		}

		// exclude_negated_tags
		if t.Filters.ExcludeNegatedTags != nil {
			for _, filterKeyVal := range *t.Filters.ExcludeNegatedTags {
				if len(filterKeyVal) == 2 {
					result[name] = append(result[name], makeElementFiltersFunction(false, true, "exclude_negated_tags", filterKeyVal[0], filterKeyVal[1]))
				} else if len(filterKeyVal) > 2 {
					result[name] = append(result[name], makeElementFiltersListFunction(false, true, "exclude_negated_tags", filterKeyVal))
				} else if len(filterKeyVal) < 2 {
					log.Errorf("mapping filter parameter error: %s  key:%s  need at least 1 more value !", "exclude_negated_tags", filterKeyVal[0])

				}
			}
		}

		// exclude_regexp_tags
		if t.Filters.ExcludeRegexpTags != nil {
			for _, filterKeyVal := range *t.Filters.ExcludeRegexpTags {
				if len(filterKeyVal) == 2 {
					result[name] = append(result[name], makeElementRegexpFiltersFunction(true, false, "exclude_regexp_tags", filterKeyVal[0], filterKeyVal[1]))
				} else {
					log.Errorf("mapping filter parameter error: %s  key:%s  need a [key],[regexpr] value !", "exclude_regexp_tags", filterKeyVal[0])
				}
			}
		}

		if t.Filters.ExcludeNegatedRegexpTags != nil {
			for _, filterKeyVal := range *t.Filters.ExcludeNegatedRegexpTags {
				if len(filterKeyVal) == 2 {
					result[name] = append(result[name], makeElementRegexpFiltersFunction(false, true, "exclude_negated_regexp_tags", filterKeyVal[0], filterKeyVal[1]))
				} else {
					log.Errorf("mapping filter parameter error: %s  key:%s  need a [key],[regexpr] value !", "exclude_negated_regexp_tags", filterKeyVal[0])
				}
			}
		}

	}
	return result
}
