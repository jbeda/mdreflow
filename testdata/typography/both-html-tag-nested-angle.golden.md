# HTML tag with a nested angle bracket

An attribute value may contain an angle bracket, which CommonMark’s own open-tag grammar allows (only the matching quote ends the value): <a title="a < b">quoted text</a>.
The tag stays intact, but “this” prose gets curled…

A single-quoted attribute value works the same way: <a title='a < b'>more text</a> and “this” prose gets curled too.
