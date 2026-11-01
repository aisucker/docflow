package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/beevik/etree"
)

const wordNamespace = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

type contentControlInfo struct {
	Tag       string `json:"tag"`
	Alias     string `json:"alias"`
	Part      string `json:"part"`
	Supported bool   `json:"supported"`
	Kind      string `json:"kind"`
}

func wordElement(e *etree.Element, name string) bool {
	return e.Tag == name && (e.NamespaceURI() == wordNamespace || e.NamespaceURI() == "http://purl.oclc.org/ooxml/wordprocessingml/main")
}
func childWord(e *etree.Element, name string) *etree.Element {
	if e == nil {
		return nil
	}
	for _, c := range e.ChildElements() {
		if wordElement(c, name) {
			return c
		}
	}
	return nil
}
func walkWord(e *etree.Element, fn func(*etree.Element)) {
	if e == nil {
		return
	}
	fn(e)
	for _, c := range e.ChildElements() {
		walkWord(c, fn)
	}
}
func wordVal(e *etree.Element) string {
	if e == nil {
		return ""
	}
	for _, a := range e.Attr {
		if a.Key == "val" {
			return a.Value
		}
	}
	return ""
}
func controlInfo(e *etree.Element, part string) contentControlInfo {
	pr := childWord(e, "sdtPr")
	content := childWord(e, "sdtContent")
	info := contentControlInfo{Tag: wordVal(childWord(pr, "tag")), Alias: wordVal(childWord(pr, "alias")), Part: part, Supported: content != nil, Kind: "inline"}
	if content != nil {
		for _, c := range content.ChildElements() {
			if wordElement(c, "p") {
				info.Kind = "block"
			} else if wordElement(c, "tbl") || wordElement(c, "tr") || wordElement(c, "tc") {
				info.Kind = "table"
				info.Supported = false
			}
		}
	}
	for _, kind := range []string{"picture", "date", "comboBox", "dropDownList", "docPartObj", "docPartList", "group", "repeatingSection", "repeatingSectionItem"} {
		if childWord(pr, kind) != nil {
			info.Supported = false
			info.Kind = kind
		}
	}
	if pr != nil {
		for _, c := range pr.ChildElements() {
			if c.Tag == "checkbox" {
				info.Supported = false
				info.Kind = "checkbox"
			}
		}
	}
	return info
}

// The original SDT properties (including Tag, Alias, lock and ID) remain intact.
// Only sdtContent is replaced, preserving the first paragraph/run formatting.
func fillContentControl(e *etree.Element, value string) error {
	info := controlInfo(e, "")
	if !info.Supported {
		return fmt.Errorf("Tag «%s»: используйте текстовый элемент управления внутри абзаца или ячейки", info.Tag)
	}
	content := childWord(e, "sdtContent")
	var runPr, paraPr *etree.Element
	hasNested := false
	walkWord(content, func(c *etree.Element) {
		if wordElement(c, "sdt") {
			hasNested = true
		}
		if runPr == nil && wordElement(c, "rPr") {
			runPr = c.Copy()
		}
		if paraPr == nil && wordElement(c, "pPr") {
			paraPr = c.Copy()
		}
	})
	if hasNested {
		return fmt.Errorf("Tag «%s» содержит вложенные элементы управления. Привяжите внутренние текстовые элементы", info.Tag)
	}
	prefix := e.Space
	if prefix == "" {
		prefix = "w"
	}
	qualified := func(name string) string { return prefix + ":" + name }
	for _, token := range append([]etree.Token(nil), content.Child...) {
		content.RemoveChild(token)
	}
	parent := content
	if info.Kind == "block" {
		parent = content.CreateElement(qualified("p"))
		if paraPr != nil {
			parent.AddChild(paraPr)
		}
	}
	run := parent.CreateElement(qualified("r"))
	if runPr != nil {
		run.AddChild(runPr)
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"), "\n")
	for n, line := range lines {
		if n > 0 {
			run.CreateElement(qualified("br"))
		}
		for i, segment := range strings.Split(line, "\t") {
			if i > 0 {
				run.CreateElement(qualified("tab"))
			}
			text := run.CreateElement(qualified("t"))
			text.CreateAttr("xml:space", "preserve")
			text.SetText(segment)
		}
	}
	pr := childWord(e, "sdtPr")
	if pr != nil {
		for _, c := range append([]*etree.Element(nil), pr.ChildElements()...) {
			if wordElement(c, "showingPlcHdr") || wordElement(c, "dataBinding") {
				pr.RemoveChild(c)
			}
		}
	}
	return nil
}
func processTemplate(src, dst string, values map[string]string, legacy map[string]string) ([]contentControlInfo, error) {
	reader, err := zip.OpenReader(src)
	if err != nil {
		return nil, fmt.Errorf("Некорректный DOCX")
	}
	defer reader.Close()
	if len(reader.File) > 1000 {
		return nil, fmt.Errorf("Слишком много частей DOCX")
	}
	var output *os.File
	var writer *zip.Writer
	if dst != "" {
		output, err = os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		defer output.Close()
		writer = zip.NewWriter(output)
		defer writer.Close()
	}
	controls := []contentControlInfo{}
	total := uint64(0)
	actualTotal := 0
	hasDocument := false
	for _, part := range reader.File {
		if strings.HasPrefix(part.Name, "/") || strings.ContainsAny(part.Name, "\\:\x00") {
			return nil, fmt.Errorf("Недопустимое имя части DOCX")
		}
		for _, segment := range strings.Split(part.Name, "/") {
			if segment == ".." {
				return nil, fmt.Errorf("Недопустимый путь внутри DOCX")
			}
		}
		total += part.UncompressedSize64
		if part.UncompressedSize64 > 30<<20 || total > 80<<20 || len(part.Name) > 256 {
			return nil, fmt.Errorf("DOCX превышает допустимый размер")
		}
		if part.Name == "word/document.xml" {
			hasDocument = true
		}
		in, err := part.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(in, 30<<20+1))
		_ = in.Close()
		if err != nil {
			return nil, err
		}
		actualTotal += len(data)
		if len(data) > 30<<20 || actualTotal > 80<<20 {
			return nil, fmt.Errorf("DOCX превышает распакованный размер")
		}
		if strings.HasPrefix(part.Name, "word/") && strings.HasSuffix(part.Name, ".xml") {
			xml := etree.NewDocument()
			if err = xml.ReadFromBytes(data); err != nil {
				return nil, fmt.Errorf("Повреждён XML в DOCX: %s", part.Name)
			}
			items := []*etree.Element{}
			walkWord(xml.Root(), func(e *etree.Element) {
				if wordElement(e, "sdt") {
					items = append(items, e)
				}
			})
			for _, item := range items {
				info := controlInfo(item, part.Name)
				if info.Tag == "" {
					continue
				}
				controls = append(controls, info)
				if value, ok := values[info.Tag]; ok {
					if err = fillContentControl(item, value); err != nil {
						return nil, err
					}
				}
			}
			// Legacy text tags only; never modify Tag attributes or generated SDT values.
			if len(legacy) > 0 {
				walkWord(xml.Root(), func(e *etree.Element) {
					if !wordElement(e, "t") {
						return
					}
					for p := e.Parent(); p != nil; p = p.Parent() {
						if wordElement(p, "sdt") {
							return
						}
					}
					text := e.Text()
					for key, value := range legacy {
						text = strings.ReplaceAll(text, "{{"+key+"}}", value)
					}
					e.SetText(text)
				})
			}
			if writer != nil {
				data, err = xml.WriteToBytes()
				if err != nil {
					return nil, err
				}
			}
		}
		if writer != nil {
			header := part.FileHeader
			header.Method = zip.Deflate
			out, err := writer.CreateHeader(&header)
			if err != nil {
				return nil, err
			}
			if _, err = out.Write(data); err != nil {
				return nil, err
			}
		}
	}
	if !hasDocument {
		return nil, fmt.Errorf("В DOCX отсутствует основной документ")
	}
	if writer != nil {
		if err = writer.Close(); err != nil {
			return nil, err
		}
		if err = output.Close(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(controls, func(i, j int) bool { return controls[i].Tag < controls[j].Tag })
	return controls, nil
}
func templateControlTags(src string) ([]string, []contentControlInfo, error) {
	infos, err := processTemplate(src, "", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	tags := []string{}
	for _, item := range infos {
		if !seen[item.Tag] {
			tags = append(tags, item.Tag)
			seen[item.Tag] = true
		}
	}
	if len(tags) > 100 {
		return nil, nil, fmt.Errorf("В одном шаблоне допускается до 100 разных Tag")
	}
	return tags, infos, nil
}
