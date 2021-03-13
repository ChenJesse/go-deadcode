package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type subjectType string

const (
	unusedFunc   subjectType = "unusedFunc"
	unusedType   subjectType = "unusedType"
	unusedMethod subjectType = "unusedMethod"
)

type unusedSubject struct {
	subjectName string
	typ         subjectType
}

type unusedFileMetadata struct {
	fileName string
	subjects map[unusedSubject]struct{}
}

type StaticCheckerJson struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Location struct {
		File   string `json:"file"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
	} `json:"location"`
	End struct {
		File   string `json:"file"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
	}
	Message string `json:"message"`
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("The file with the staticcheck tool output must be passed in.")
	}
	staticcheckFile, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	fileSet := map[string]struct{}{}
	unusedMetadata := map[string]*unusedFileMetadata{}
	unusedRegexp := regexp.MustCompile(`(.*)\ (.*)\ is\ unused.*`)

	scanner := bufio.NewScanner(staticcheckFile)
	for scanner.Scan() {
		var violation StaticCheckerJson
		if err := json.Unmarshal(scanner.Bytes(), &violation); err != nil {
			log.Fatal(err)
		}
		if unusedRegexp.MatchString(violation.Message) {
			m := unusedRegexp.FindStringSubmatch(violation.Message)
			fileName := violation.Location.File
			typ, subject := m[1], m[2]
			metadata := unusedMetadata[fileName]
			if metadata == nil {
				metadata = &unusedFileMetadata{
					fileName: fileName,
					subjects: map[unusedSubject]struct{}{},
				}
			}

			switch typ {
			case "func":
				// If this function has a `.`, it is a method.
				if strings.Contains(subject, ".") {
					methodName := subject
					metadata.subjects[unusedSubject{methodName, unusedMethod}] = struct{}{}
					unusedMetadata[fileName] = metadata
					fileSet[fileName] = struct{}{}
					continue
				}
				funcName := subject
				// Let's not delete main functions, they could be run manually.
				if funcName == "main" {
					continue
				}
				metadata.subjects[unusedSubject{funcName, unusedFunc}] = struct{}{}
				unusedMetadata[fileName] = metadata
				fileSet[fileName] = struct{}{}
			case "type":
				typeName := subject
				metadata.subjects[unusedSubject{typeName, unusedType}] = struct{}{}
				unusedMetadata[fileName] = metadata
				fileSet[fileName] = struct{}{}
			}
		}
	}

	handleUnused(fileSet, unusedMetadata)
}

func handleUnused(fileSet map[string]struct{}, unusedMetadata map[string]*unusedFileMetadata) {
	for fileName := range fileSet {
		if strings.Contains(fileName, "generate") {
			// We don't want to touch generated files.
			continue
		}
		log.Printf("Processing file: %s...", fileName)
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, filepath.Join(fileName), nil, parser.ParseComments)
		if err != nil {
			log.Fatal(err)
		}
		var usedDecls []ast.Decl
		cmap := ast.NewCommentMap(fset, node, node.Comments)
		thisMetadata := unusedMetadata[fileName]
		for _, decl := range node.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok {
				// If the function is unused, remove it.
				if _, ok := thisMetadata.subjects[unusedSubject{fn.Name.Name, unusedFunc}]; ok {
					continue
				}

				// If this is a method and the receiver type is unused, remove the method.
				if fn.Recv != nil {
					var receiverTypeName string
					switch typ := fn.Recv.List[0].Type.(type) {
					case *ast.StarExpr:
						if si, ok := typ.X.(*ast.Ident); ok {
							receiverTypeName = si.Name
						}
					case *ast.Ident:
						receiverTypeName = typ.Name
					}
					if _, ok := thisMetadata.subjects[unusedSubject{receiverTypeName, unusedType}]; ok {
						continue
					}
					if _, ok := thisMetadata.subjects[unusedSubject{fmt.Sprintf("%s.%s", receiverTypeName, fn.Name.Name), unusedMethod}]; ok {
						continue
					}
				}
			}

			gn, ok := decl.(*ast.GenDecl)
			if ok {
				if typeSpec, ok := gn.Specs[0].(*ast.TypeSpec); ok {
					// If the struct is unused, remove it.
					if _, ok = thisMetadata.subjects[unusedSubject{typeSpec.Name.Name, unusedType}]; ok {
						continue
					}
				}
			}

			usedDecls = append(usedDecls, decl)
			node.Decls = usedDecls
			// Delete comments associated with the removed functions.
			node.Comments = cmap.Filter(node).Comments()
		}

		var buf bytes.Buffer
		err = format.Node(&buf, fset, node)
		if err != nil {
			log.Fatal(err)
		}

		err = ioutil.WriteFile(fileName, buf.Bytes(), 0)
		if err != nil {
			log.Fatal(err)
		}
	}
}


