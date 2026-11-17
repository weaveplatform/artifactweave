These byte fixtures use JDOM 2.0.6 `new XMLOutputter()` and the cleanup predicates
from Artifactory OSS 7.161.26 `PomTransformer`. `raw.xml` covers the declaration,
namespace/attribute order, comments, whitespace, escaping, empty elements and
CDATA. `profiles.xml` distinguishes default-active, whitespace-padded activation
and opt-in profiles. `attrs.xml` distinguishes literal attribute whitespace
from numeric character references, including a multiline schemaLocation.

The expected files contain CRLF output bytes, preserved by `.gitattributes`.
The source inputs use LF; JDOM normalizes XML input line endings before output.

To regenerate in a disposable directory with JDOM 2.0.6 on the classpath:

```sh
javac -cp jdom2-2.0.6.jar PomRaw.java
java -cp .:jdom2-2.0.6.jar PomRaw raw.xml > raw.expected.xml
java -cp .:jdom2-2.0.6.jar PomRaw profiles.xml > profiles.expected.xml
java -cp .:jdom2-2.0.6.jar PomRaw attrs.xml > attrs.expected.xml
```

Production and ordinary tests need no Java runtime. The verified Java runner was
`eclipse-temurin:17-jdk-jammy` at digest
`sha256:76751c16a2a07ec0960e659a2603491f8c79d95592173f6c0df8bf8ebab257cc`.
The bundled JDOM version remains a P19 comparison case, as ADR0036 records.
