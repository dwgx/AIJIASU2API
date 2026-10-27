# Third-Party Notices

This file lists the third-party software that is compiled into, or served by,
the release artifacts of this repository:

- `ajs-accountd` binaries (`ajs-accountd-linux-amd64`,
  `ajs-accountd-windows-amd64.exe`, `ajs-accountd-darwin-arm64`) and the
  `ajs-accountd` container image;
- the dashboard bundle (`dashboard-dist.tar.gz`) and the `ajs-dashboard`
  container image;
- the operator deploy kit (`deploy-kit.tar.gz`).

The repository's own code is MIT-licensed; see `LICENSE`. `NOTICE` records
additional attributions and the geographic-data terms. This file reproduces
the license texts required for redistribution: MIT, ISC, BSD-3-Clause and the
SIL Open Font License 1.1.

Development-only npm packages (`vite`, `vitest`, `jsdom`, TypeScript, Testing
Library and the `@vitejs/plugin-react` toolchain) are not part of the served
bundle or the images; their license texts ship with the npm packages. The
`@types/*` packages are type declarations only and emit no runtime code.

## 1. Go runtime and modules (daemon binary)

| Component | Version | License | Copyright |
| --- | --- | --- | --- |
| Go standard library | Go 1.26.10 | BSD-3-Clause | © 2009 The Go Authors |
| `golang.org/x/crypto` | v0.58.0 | BSD-3-Clause | © 2009 The Go Authors |
| `golang.org/x/sys` | v0.49.0 (indirect) | BSD-3-Clause | © 2009 The Go Authors |

The daemon is built with `CGO_ENABLED=0`; no third-party C library is linked.
The Go runtime and standard library are covered by the BSD-3-Clause text in
section 3.3. `golang.org/x/crypto` is used for `argon2`; `golang.org/x/sys`
provides the platform syscall layer.

## 2. Dashboard runtime dependencies

Versions are the exact resolved versions in `dashboard/package-lock.json`.
These packages are bundled into the served JavaScript/CSS or the font files
that the dashboard ships.

| Package | Version | License |
| --- | --- | --- |
| `@babel/runtime` | 7.29.10 | MIT |
| `@fontsource/inter` | 5.3.0 | OFL-1.1 |
| `@fontsource/jetbrains-mono` | 5.3.0 | OFL-1.1 |
| `@types/d3-array` | 3.2.2 | MIT |
| `@types/d3-color` | 3.1.3 | MIT |
| `@types/d3-ease` | 3.0.2 | MIT |
| `@types/d3-interpolate` | 3.0.4 | MIT |
| `@types/d3-path` | 3.1.1 | MIT |
| `@types/d3-scale` | 4.0.9 | MIT |
| `@types/d3-shape` | 3.2.0 | MIT |
| `@types/d3-time` | 3.0.4 | MIT |
| `@types/d3-timer` | 3.0.2 | MIT |
| `clsx` | 2.1.1 | MIT |
| `cookie` | 1.1.1 | MIT |
| `csstype` | 3.2.3 | MIT |
| `d3-array` | 3.2.4 | ISC |
| `d3-color` | 3.1.0 | ISC |
| `d3-ease` | 3.0.1 | BSD-3-Clause |
| `d3-format` | 3.1.2 | ISC |
| `d3-interpolate` | 3.0.1 | ISC |
| `d3-path` | 3.1.0 | ISC |
| `d3-scale` | 4.0.2 | ISC |
| `d3-shape` | 3.2.0 | ISC |
| `d3-time` | 3.1.0 | ISC |
| `d3-time-format` | 4.1.0 | ISC |
| `d3-timer` | 3.0.1 | ISC |
| `decimal.js-light` | 2.5.1 | MIT |
| `dom-helpers` | 5.2.1 | MIT |
| `eventemitter3` | 4.0.7 | MIT |
| `fast-equals` | 5.4.5 | MIT |
| `internmap` | 2.0.3 | ISC |
| `js-tokens` | 4.0.0 | MIT |
| `lodash` | 4.18.1 | MIT |
| `loose-envify` | 1.4.0 | MIT |
| `lucide-react` | 0.468.0 | ISC (portions MIT, Feather) |
| `object-assign` | 4.1.1 | MIT |
| `prop-types` | 15.8.1 | MIT |
| `react` | 19.3.1 | MIT |
| `react-dom` | 19.3.1 | MIT |
| `react-is` | 16.13.1 | MIT |
| `react-is` | 18.3.1 | MIT |
| `react-router` | 7.18.4 | MIT |
| `react-router-dom` | 7.18.4 | MIT |
| `react-smooth` | 4.0.4 | MIT |
| `react-transition-group` | 4.4.5 | BSD-3-Clause |
| `recharts` | 2.15.4 | MIT |
| `recharts-scale` | 0.4.5 | MIT |
| `scheduler` | 0.28.0 | MIT |
| `set-cookie-parser` | 2.7.3 | MIT |
| `tiny-invariant` | 1.3.3 | MIT |
| `victory-vendor` | 36.9.2 | MIT AND ISC |

Copyright notices for the main families:

- React, React DOM, Scheduler — © Meta Platforms, Inc. and affiliates.
- React Router — © React Training LLC 2015-2019; Remix Software Inc.
  2020-2021; Shopify Inc. 2022-2023.
- Recharts, React Smooth, Recharts Scale — © 2015-present recharts;
  © 2016 recharts; © 2015 Sen Yang respectively.
- D3 modules — © 2010-2023 Mike Bostock (`d3-ease` also © 2001 Robert
  Penner).
- Lucide — ISC, © Lucide Contributors 2022; portions from Feather are MIT,
  © Cole Bemis 2013-2022.
- `react-transition-group` — BSD-3-Clause, © 2018 React Community; forked
  from React, © 2013-present Facebook, Inc.
- `clsx` — MIT, © Luke Edwards (lukeed.com).
- `@fontsource/inter` — OFL-1.1, © 2016 The Inter Project Authors.
- `@fontsource/jetbrains-mono` — OFL-1.1, © 2020 The JetBrains Mono Project
  Authors.
- `victory-vendor` — MIT AND ISC, © Formidable.

`victory-vendor` is distributed under `MIT AND ISC`; both texts apply and are
reproduced in sections 3.1 and 3.2.

The bundled font files are governed by the SIL Open Font License 1.1
(section 3.4), which requires the copyright notice and license to travel with
the font software. This file is included in the dashboard archive and image
for that purpose.

## 3. License texts

### 3.1 MIT License

The project and the MIT-licensed third-party components above are covered by
the following permission text (the project's own copyright line is in
`LICENSE`; each third-party copyright line is listed in sections 1 and 2):

```text
MIT License

Copyright (c) 2026 dwgx

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### 3.2 ISC License

```text
ISC License

Copyright (c) for portions of Lucide are held by Cole Bemis 2013-2022 as part
of Feather (MIT). All other copyright (c) for Lucide are held by Lucide
Contributors 2022.

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
```

The D3 modules are ISC-licensed with © 2010-2023 Mike Bostock; `internmap`
is ISC-licensed with © 2021 Mike Bostock. The same permission text applies.

### 3.3 BSD-3-Clause License

```text
Copyright (c) 2009 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

`d3-ease` (© 2010-2021 Mike Bostock; © 2001 Robert Penner) and
`react-transition-group` (© 2018 React Community; forked from React,
© 2013-present Facebook, Inc.) use the same BSD-3-Clause permission text with
their own copyright lines.

### 3.4 SIL Open Font License 1.1

```text
Copyright 2016 The Inter Project Authors (https://github.com/rsms/inter)
Copyright 2020 The JetBrains Mono Project Authors
(https://github.com/JetBrains/JetBrainsMono)

This Font Software is licensed under the SIL Open Font License, Version 1.1.
This license is copied below, and is also available with a FAQ at:
http://scripts.sil.org/OFL


-----------------------------------------------------------
SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007
-----------------------------------------------------------

PREAMBLE
The goals of the Open Font License (OFL) are to stimulate worldwide
development of collaborative font projects, to support the font creation
efforts of academic and linguistic communities, and to provide a free and
open framework in which fonts may be shared and improved in partnership
with others.

The OFL allows the licensed fonts to be used, studied, modified and
redistributed freely as long as they are not sold by themselves. The
fonts, including any derivative works, can be bundled, embedded,
redistributed and/or sold with any software provided that any reserved
names are not used by derivative works. The fonts and derivatives,
however, cannot be released under any other type of license. The
requirement for fonts to remain under this license does not apply
to any document created using the fonts or their derivatives.

DEFINITIONS
"Font Software" refers to the set of files released by the Copyright
Holder(s) under this license and clearly marked as such. This may
include source files, build scripts and documentation.

"Reserved Font Name" refers to any names specified as such after the
copyright statement(s).

"Original Version" refers to the collection of Font Software components as
distributed by the Copyright Holder(s).

"Modified Version" refers to any derivative made by adding to, deleting,
or substituting -- in part or in whole -- any of the components of the
Original Version, by changing formats or by porting the Font Software to a
new environment.

"Author" refers to any designer, engineer, programmer, technical
writer or other person who contributed to the Font Software.

PERMISSION & CONDITIONS
Permission is hereby granted, free of charge, to any person obtaining
a copy of the Font Software, to use, study, copy, merge, embed, modify,
redistribute, and sell modified and unmodified copies of the Font
Software, subject to the following conditions:

1) Neither the Font Software nor any of its individual components,
in Original or Modified Versions, may be sold by itself.

2) Original or Modified Versions of the Font Software may be bundled,
redistributed and/or sold with any software, provided that each copy
contains the above copyright notice and this license. These can be
included either as stand-alone text files, human-readable headers or
in the appropriate machine-readable metadata fields within text or
binary files as long as those fields can be easily viewed by the user.

3) No Modified Version of the Font Software may use the Reserved Font
Name(s) unless explicit written permission is granted by the corresponding
Copyright Holder. This restriction only applies to the primary font name as
presented to the users.

4) The name(s) of the Copyright Holder(s) or the Author(s) of the Font
Software shall not be used to promote, endorse or advertise any
Modified Version, except to acknowledge the contribution(s) of the
Copyright Holder(s) and the Author(s) or with their explicit written
permission.

5) The Font Software, modified or unmodified, in part or in whole,
must be distributed entirely under this license, and must not be
distributed under any other license. The requirement for fonts to
remain under this license does not apply to any document created
using the Font Software.

TERMINATION
This license becomes null and void if any of the above conditions are
not met.

DISCLAIMER
THE FONT SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO ANY WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT
OF COPYRIGHT, PATENT, TRADEMARK, OR OTHER RIGHT. IN NO EVENT SHALL THE
COPYRIGHT HOLDER BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
INCLUDING ANY GENERAL, SPECIAL, INDIRECT, INCIDENTAL, OR CONSEQUENTIAL
DAMAGES, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
FROM, OUT OF THE USE OR INABILITY TO USE THE FONT SOFTWARE OR FROM
OTHER DEALINGS IN THE FONT SOFTWARE.
```

## 4. Geographic data

The dashboard map assets and the daemon city index are derived from DataV
GeoAtlas / AMAP data and are **not** MIT-licensed. Their source, version and
terms are recorded in `NOTICE` section 3. Anyone redistributing the dashboard
bundle or image should review the current DataV and AMAP terms first.
