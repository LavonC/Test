import docx
from docx.shared import Inches, Pt, RGBColor
from docx.enum.text import WD_ALIGN_PARAGRAPH
from docx.enum.table import WD_TABLE_ALIGNMENT
from docx.oxml import parse_xml
from docx.oxml.ns import nsdecls

def generate_exact_3page_doc():
    doc = docx.Document()

    # Page Margins: 0.65 in
    for section in doc.sections:
        section.top_margin = Inches(0.65)
        section.bottom_margin = Inches(0.65)
        section.left_margin = Inches(0.65)
        section.right_margin = Inches(0.65)
        
        header = section.header
        hp = header.paragraphs[0]
        hp.alignment = WD_ALIGN_PARAGRAPH.RIGHT
        hrun = hp.add_run("UniCompute • Objective 1: P2P Substrate Literature Review & Analysis")
        hrun.font.name = "Calibri"
        hrun.font.size = Pt(8.0)
        hrun.font.color.rgb = RGBColor(120, 120, 120)

        footer = section.footer
        fp = footer.paragraphs[0]
        fp.alignment = WD_ALIGN_PARAGRAPH.CENTER
        frun = fp.add_run("Final Year Project Specification • Objective 1 Technical Module (Page 1 to 3)")
        frun.font.name = "Calibri"
        frun.font.size = Pt(8.0)
        frun.font.color.rgb = RGBColor(120, 120, 120)

    COLOR_PRIMARY = RGBColor(27, 54, 93)     # Deep Navy #1B365D
    COLOR_SECONDARY = RGBColor(46, 91, 136)  # Slate Blue #2E5B88
    COLOR_DARK = RGBColor(40, 40, 40)        # Body text
    HEX_PRIMARY = "1B365D"
    HEX_LIGHT_BG = "F0F4F8"
    HEX_ALT_ROW = "F9FAFB"

    # Base styling
    normal_style = doc.styles['Normal']
    normal_style.font.name = 'Calibri'
    normal_style.font.size = Pt(9.0)
    normal_style.font.color.rgb = COLOR_DARK
    normal_style.paragraph_format.line_spacing = 1.08
    normal_style.paragraph_format.space_after = Pt(2)

    def set_cell_shading(cell, color_hex):
        shading = parse_xml(f'<w:shd {nsdecls("w")} w:fill="{color_hex}"/>')
        cell._tc.get_or_add_tcPr().append(shading)

    def set_cell_margins(cell, top=40, bottom=40, left=60, right=60):
        tcPr = cell._tc.get_or_add_tcPr()
        tcMar = parse_xml(f'<w:tcMar {nsdecls("w")}><w:top w:w="{top}" w:type="dxa"/><w:bottom w:w="{bottom}" w:type="dxa"/><w:left w:w="{left}" w:type="dxa"/><w:right w:w="{right}" w:type="dxa"/></w:tcMar>')
        tcPr.append(tcMar)

    def add_h1(text, space_before=6):
        p = doc.add_paragraph()
        p.paragraph_format.space_before = Pt(space_before)
        p.paragraph_format.space_after = Pt(1)
        p.paragraph_format.keep_with_next = True
        run = p.add_run(text)
        run.font.name = 'Calibri'
        run.font.size = Pt(11)
        run.font.bold = True
        run.font.color.rgb = COLOR_PRIMARY
        return p

    def add_h2(text, space_before=4):
        p = doc.add_paragraph()
        p.paragraph_format.space_before = Pt(space_before)
        p.paragraph_format.space_after = Pt(1)
        p.paragraph_format.keep_with_next = True
        run = p.add_run(text)
        run.font.name = 'Calibri'
        run.font.size = Pt(9.5)
        run.font.bold = True
        run.font.color.rgb = COLOR_SECONDARY
        return p

    def add_p(text, bold_prefix=None, space_after=2):
        p = doc.add_paragraph()
        p.paragraph_format.space_before = Pt(0)
        p.paragraph_format.space_after = Pt(space_after)
        p.paragraph_format.line_spacing = 1.08
        if bold_prefix:
            r_pre = p.add_run(bold_prefix)
            r_pre.font.bold = True
            r_pre.font.color.rgb = COLOR_PRIMARY
        r = p.add_run(text)
        r.font.size = Pt(9.0)
        return p

    def add_bullet(text, bold_prefix=None):
        p = doc.add_paragraph(style='List Bullet')
        p.paragraph_format.space_before = Pt(0)
        p.paragraph_format.space_after = Pt(1.5)
        p.paragraph_format.line_spacing = 1.05
        if bold_prefix:
            r_pre = p.add_run(bold_prefix)
            r_pre.font.bold = True
            r_pre.font.size = Pt(8.5)
            r_pre.font.color.rgb = COLOR_PRIMARY
        r = p.add_run(text)
        r.font.size = Pt(8.5)
        return p

    def create_compact_table(headers, rows, col_widths=None):
        tbl = doc.add_table(rows=len(rows)+1, cols=len(headers))
        tbl.alignment = WD_TABLE_ALIGNMENT.CENTER
        
        # Header
        hdr_cells = tbl.rows[0].cells
        for i, header_text in enumerate(headers):
            cell = hdr_cells[i]
            set_cell_shading(cell, HEX_PRIMARY)
            set_cell_margins(cell, top=40, bottom=40, left=50, right=50)
            p = cell.paragraphs[0]
            p.paragraph_format.space_before = Pt(0)
            p.paragraph_format.space_after = Pt(0)
            r = p.add_run(header_text)
            r.font.name = 'Calibri'
            r.font.size = Pt(8.0)
            r.font.bold = True
            r.font.color.rgb = RGBColor(255, 255, 255)
            
        # Data
        for row_idx, row_data in enumerate(rows):
            row_cells = tbl.rows[row_idx + 1].cells
            bg_color = HEX_ALT_ROW if row_idx % 2 == 1 else "FFFFFF"
            for col_idx, cell_value in enumerate(row_data):
                cell = row_cells[col_idx]
                set_cell_shading(cell, bg_color)
                set_cell_margins(cell, top=30, bottom=30, left=50, right=50)
                p = cell.paragraphs[0]
                p.paragraph_format.space_before = Pt(0)
                p.paragraph_format.space_after = Pt(0)
                p.paragraph_format.line_spacing = 1.02
                r = p.add_run(cell_value)
                r.font.name = 'Calibri'
                r.font.size = Pt(7.5)
                r.font.color.rgb = COLOR_DARK

        if col_widths:
            for row in tbl.rows:
                for idx, w in enumerate(col_widths):
                    row.cells[idx].width = Inches(w)
                    
        p_space = doc.add_paragraph()
        p_space.paragraph_format.space_before = Pt(0)
        p_space.paragraph_format.space_after = Pt(1)

    # =========================================================================
    # PAGE 1: Scope, Context & Literature Foundations
    # =========================================================================
    p_title = doc.add_paragraph()
    p_title.paragraph_format.space_before = Pt(0)
    p_title.paragraph_format.space_after = Pt(1)
    r_t = p_title.add_run("UniCompute Objective 1: P2P Transport Substrate, Edge CAS Vault & Execution Fleet")
    r_t.font.name = 'Calibri'
    r_t.font.size = Pt(14)
    r_t.font.bold = True
    r_t.font.color.rgb = COLOR_PRIMARY

    p_sub = doc.add_paragraph()
    p_sub.paragraph_format.space_before = Pt(0)
    p_sub.paragraph_format.space_after = Pt(3)
    r_s = p_sub.add_run("Literature Review & Technical Systems Analysis • Dedicated Subsystem Specification (Objective 1)")
    r_s.font.size = Pt(9.5)
    r_s.font.color.rgb = COLOR_SECONDARY

    # Metadata Strip
    meta_tbl = doc.add_table(rows=1, cols=4)
    meta_tbl.alignment = WD_TABLE_ALIGNMENT.CENTER
    meta_cells = meta_tbl.rows[0].cells
    meta_items = [
        ("Platform Scope", "Objective 1 (Member A) Exclusively"),
        ("Core Protocols", "WebRTC / SCTP / ICE / UNIR Wire"),
        ("Host Boundary", "Air-Gapped Docker (--network none)"),
        ("Key Invariant", "CP-1 (Data Plane Autonomy)")
    ]
    for idx, (label, val) in enumerate(meta_items):
        cell = meta_cells[idx]
        set_cell_shading(cell, HEX_LIGHT_BG)
        set_cell_margins(cell, top=25, bottom=25, left=40, right=40)
        p = cell.paragraphs[0]
        p.paragraph_format.space_before = Pt(0)
        p.paragraph_format.space_after = Pt(0)
        r1 = p.add_run(label + "\n")
        r1.font.bold = True
        r1.font.size = Pt(7.0)
        r1.font.color.rgb = COLOR_PRIMARY
        r2 = p.add_run(val)
        r2.font.size = Pt(7.5)
        r2.font.color.rgb = COLOR_DARK
    
    doc.add_paragraph().paragraph_format.space_after = Pt(1)

    add_h1("1. Problem Formulation & Objective 1 Operational Scope", space_before=4)
    add_p(
        "Classical volunteer computing platforms like BOINC [1] and Folding@home [2] rely on a client-server architecture where worker "
        "nodes pull task inputs from a centralized HTTP repository and push outputs back. While effective for compute-bound batch jobs with "
        "minimal inputs, this model fails under modern data-intensive workloads. In campus and residential environments, home broadband "
        "standards (DOCSIS 3.0/3.1 and asymmetric DSL) allocate substantially narrower spectrum to upstream channels, creating a typical 10:1 "
        "downstream-to-upstream asymmetry that severely penalizes central uploads. Concurrently, institutional server bandwidth becomes a critical bottleneck."
    )
    add_p(
        "Objective 1 establishes the underlying peer-to-peer data transport substrate, content-addressed storage (CAS) vault, and "
        "sandboxed worker execution fleet for UniCompute. It provides the core data plane while strictly isolating itself from higher-order "
        "distributed algorithms (multi-node Cauchy Reed-Solomon erasure coding, FastCDC chunk deduplication, client convergent encryption, and global "
        "task schedulers), which belong to Objectives 2–5. Specifically, Objective 1 owns: (1) full WebRTC P2P DataChannel lifecycle management, "
        "(2) dual-stack NAT and Carrier-Grade NAT (CGNAT) traversal, (3) direct UDP hole punching with blind contributor relay fallback, "
        "(4) node-local content-addressed storage with byte quotas and atomic renames, (5) air-gapped container execution, and (6) empirical "
        "proof of Invariant CP-1 (complete data plane autonomy under coordinator failure).",
        bold_prefix="Scope and Separation of Concerns: "
    )

    add_h1("2. Theoretical Foundations & Comprehensive Literature Review", space_before=5)
    add_h2("2.1 NAT Traversal, ICE (RFC 8445) & Carrier-Grade NAT (RFC 6598)")
    add_p(
        "Network Address Translation (NAT, RFC 1918) creates stateful middleboxes that drop uninvited incoming packets. "
        "The Interactive Connectivity Establishment framework (ICE, RFC 8445 [8]) discovers connectivity via STUN (RFC 5389 [9]) and "
        "TURN (RFC 5766 [10]), prioritizing candidate pairs via: Priority = 2^24*(type_pref) + 2^8*(local_pref) + (256 - component_ID). "
        "However, residential ISPs extensively deploy Carrier-Grade NAT (CGNAT, RFC 6598 [5], prefix 100.64.0.0/10), which enforces Endpoint-Dependent "
        "Mapping (Symmetric NAT, RFC 4787 [11]). Because dynamic external ports cannot be predicted by an external peer, classical IPv4 UDP hole "
        "punching between two CGNAT-bound endpoints fails deterministically without out-of-band coordination."
    )
    add_p(
        "Objective 1 overcomes this barrier by exploiting global IPv6 adoption. Empirical telemetry from APNIC Labs [4] shows ~78.6% IPv6 capability "
        "in emerging broadband markets like India. Because IPv6 addresses are globally routable and un-NATted, Objective 1 executes parallel dual-stack "
        "STUN harvesting. When native IPv6 is present, nodes negotiate direct end-to-end peer bindings within a single round-trip time (1 RTT direct STUN check), "
        "completely bypassing the IPv4 CGNAT middlebox layer.",
        bold_prefix="Dual-Stack IPv6 Traversal Advantage: "
    )

    add_h2("2.2 Decentralized Relays & High-Throughput SCTP Transport")
    add_p(
        "When symmetric firewalls prevent direct hole punching on both endpoints, traffic requires relaying. Operating centralized cloud TURN "
        "servers incurs severe egress bandwidth expenses. Objective 1 adopts the Syncthing relay-v1 model [12]: volunteer contributor nodes donate "
        "spare network capacity as blind, opaque TCP forwarders. Relays pair peers via 128-bit cryptographic tokens without inspecting or decrypting payloads."
    )
    add_p(
        "Transport across established links uses WebRTC DataChannels (RFC 8831 [13]), encapsulating SCTP (RFC 4960 [14]) over DTLS 1.3. "
        "To prevent Head-of-Line blocking, Objective 1 multiplexes two independent channels: an ordered 'chunk' stream for storage blobs and a low-latency "
        "'stream' channel for interactive logs. Crucially, maximum transport throughput is governed by the Bandwidth-Delay Product (BDP) receive window: "
        "Throughput_max <= W_recv / RTT (RFC 1323 [15]). Standard WebRTC stacks configure a conservative 1 MiB SCTP receive buffer, which over a 100 ms WAN "
        "imposes an unyielding ceiling of (1,048,576 * 8) / 0.1s = 83.88 Mbps (~10.48 MB/s). Objective 1 scales the SCTP receive buffer to 16 MiB, raising "
        "the theoretical window ceiling to 1.34 Gbps and saturating gigabit residential fiber connections.",
        bold_prefix="BDP Window Sizing (16 MiB SCTP Buffer): "
    )

    # PAGE BREAK TO PAGE 2
    doc.add_page_break()

    # =========================================================================
    # PAGE 2: Architecture & Invariant CP-1
    # =========================================================================
    add_h2("2.3 Content-Addressed Storage Atomicity & Container Sandboxing Gradients", space_before=2)
    add_p(
        "Content-Addressed Storage (CAS) indexes blocks by cryptographic digests: CID = SHA-256(Payload). On consumer laptops, abrupt lid closures "
        "during writes risk corrupting storage. Objective 1 enforces two-phase atomic commits: incoming streams write to a scratch directory (tmp/), "
        "compute digests on the fly via io.TeeReader, and execute an atomic directory rename (os.Rename) into active storage (data/), exploiting POSIX and "
        "NTFS metadata atomicity. For compute sandboxing, whereas Golem [3] requires heavy virtual machines (>1.8 GB idle RAM, BIOS virtualization), "
        "Objective 1 utilizes lightweight, air-gapped OCI containers hardened by Linux kernel namespaces and cgroups v2 [16].",
        bold_prefix="CAS Atomicity & Sandboxing: "
    )

    add_h1("3. Objective 1 Subsystem Architecture & Implementation", space_before=5)
    add_p(
        "Objective 1 is implemented as a standalone, CGO-free Go module compiled into three binaries: unicompute-node (worker daemon), "
        "unicompute-coordinator (signaling hub), and unicompute-relay (blind forwarder). Its architecture comprises six primary subsystems:"
    )
    add_bullet("Connects over outbound WebSockets (port 443). To counter consumer NAT mapping timeouts (which RFC 4787 [11] and edge measurement literature [17] show occur within 30–60s), the client runs a 20s ping ticker, a 60s read deadline, and auto-reconnects with exponential backoff (1–8s).", bold_prefix="1. Resilient Signaling (pkg/signaling): ")
    add_bullet("Parses RFC 6598 CIDR ranges (100.64.0.0/10) and probes dual-stack STUN endpoints. To prevent RFC 8445 retransmission timeouts across unroutable candidates, nat.IsVirtualInterface() prunes Hyper-V, WSL2, Docker, and VPN adapters before gathering. Traps Win32 socket resets (WSAECONNRESET 10054 / WSAENETRESET 10052) as non-fatal warnings.", bold_prefix="2. Dual-Stack NAT Classifier (pkg/nat): ")
    add_bullet("Configures Pion WebRTC with a 16 MiB SCTP buffer and 15s ICE keepalives. Chunks are framed into 60–128 KiB segments and gated by dc.BufferedAmount() against a 4 MiB threshold to prevent worker RAM bloat. Falls back to relays if direct hole punching stalls past 2.0s.", bold_prefix="3. WebRTC Data Transport (pkg/transport): ")
    add_bullet("Implements the binary UNIR protocol (magic 0x554E4952, v0.01). Relays match communicating pairs via 128-bit random tokens, enforce contributor rate limits (RateLimitBytes), and execute zero-copy blind TCP splicing.", bold_prefix="4. Blind Contributor Relay (pkg/relay): ")
    add_bullet("Manages disk storage with SHA-256 validation, path traversal sanitation (validateID), two-phase atomic staging, real-time byte quotas with instant deletion reclamation, and automated bit-rot scrubbing (Scrub()).", bold_prefix="5. Content-Addressed Vault (pkg/blobstore): ")
    add_bullet("Executes untrusted C, C++, and Python workloads inside short-lived Docker containers: --network none (air-gap), --read-only root filesystem, --cap-drop ALL, --security-opt no-new-privileges:true, --memory 128m --memory-swap 128m, --cpus 1.0, --pids-limit 64, nobody UID 65534, ephemeral 64 MB tmpfs, 1–30s timeout with forced container cleanup (docker rm -f), and 3 KiB capped stdout/stderr buffers.", bold_prefix="6. Air-Gapped Code Sandbox (pkg/codeexec): ")

    add_h1("4. Invariant CP-1 Verification: Data Plane Independence", space_before=5)
    add_p(
        "Invariant CP-1 dictates that established P2P data channels, chunk transfers, and local storage operations must continue operating "
        "without interruption if the central coordinator or signaling hub crashes. This was empirically proven in pkg/node/node_test.go:"
    )

    # Compact Callout Box
    tbl_code = doc.add_table(rows=1, cols=1)
    tbl_code.alignment = WD_TABLE_ALIGNMENT.CENTER
    c_cell = tbl_code.cell(0, 0)
    set_cell_shading(c_cell, HEX_LIGHT_BG)
    set_cell_margins(c_cell, top=35, bottom=35, left=60, right=60)
    borders = parse_xml(f'<w:tcBorders {nsdecls("w")}><w:top w:val="none"/><w:left w:val="single" w:sz="18" w:space="0" w:color="{HEX_PRIMARY}"/><w:bottom w:val="none"/><w:right w:val="none"/></w:tcBorders>')
    c_cell._tc.get_or_add_tcPr().append(borders)
    cp = c_cell.paragraphs[0]
    cp.paragraph_format.space_before = Pt(0)
    cp.paragraph_format.space_after = Pt(0)
    r_code = cp.add_run(
        "// Integration Test: pkg/node/node_test.go (TestNode_InvariantCP1_CoordinatorFailureResilience)\n"
        "1. Nodes 1 & 2 register with coordinator hub; WebRTC SCTP 'chunk' and 'stream' DataChannels open.\n"
        "2. COORDINATOR TERMINATION: hub.Close() is invoked; central WebSocket signaling hub is killed.\n"
        "3. AUTONOMOUS TRANSFER: Node 1 stores new chunk and executes TransferChunk(ctx, \"Node-2\", chunkCID).\n"
        "4. Node 2 receives chunk over direct WebRTC channel, validates SHA-256 hash, and commits to disk.\n"
        "Result: PASS — 100% data plane autonomy confirmed; zero packet loss or connection drop upon hub termination."
    )
    r_code.font.name = "Consolas"
    r_code.font.size = Pt(7.5)
    r_code.font.color.rgb = COLOR_DARK

    doc.add_paragraph().paragraph_format.space_after = Pt(1)
    add_p(
        "The entire substrate is exposed via the clean node.Substrate Go interface (PutBlob, GetBlob, HasBlob, TransferChunk, FetchChunk, "
        "SendStream, GetPathMetrics, and NATReport). A thread-safe in-memory mock (node.StubNode) allows higher objectives to test without live networking.",
        bold_prefix="Unified Substrate API: "
    )

    # PAGE BREAK TO PAGE 3
    doc.add_page_break()

    # =========================================================================
    # PAGE 3: Defect Fixes, Comparison & References
    # =========================================================================
    add_h1("5. Empirical Defect Remediations & Technical Solutions", space_before=2)
    add_p("Seven empirical edge defects were identified and resolved during real-world broadband testing:")

    create_compact_table(
        headers=["Defect Identified", "Observed Root Cause", "Technical Resolution", "Architectural Outcome"],
        rows=[
            ["1. Silent NAT Drops", "NATs drop idle UDP state within 30-60s (RFC 4787 [11]).", "20s WebSocket ping + 60s read deadline + 15s ICE keepalives.", "Maintains state mappings; zero idle drops."],
            ["2. WAN BDP Choke", "Default 1 MiB SCTP buffer capped 100ms WAN to ~83.9 Mbps.", "Explicitly scaled SCTP receive buffer to 16 MiB in Pion engine.", "Lifts ceiling to 1.34 Gbps, saturating fiber."],
            ["3. Worker RAM Bloat", "Unconstrained chunk bursts flooded userspace WebRTC queues.", "60-128 KiB framing + 4 MiB dc.BufferedAmount() flow control.", "Enforces deterministic <32 MB memory bounds."],
            ["4. IPv4 CGNAT Stall", "Endpoint-dependent ISP mapping (100.64.0.0/10) blocked UDP.", "Parallel dual-stack STUN; direct IPv6 hole punching prioritized.", "Direct P2P established in 1 RTT over IPv6."],
            ["5. Stalled ICE Checks", "Hyper-V, WSL2, Docker, and VPN adapters flooded unroutable IPs.", "nat.IsVirtualInterface() filters virtual NICs before ICE gathering.", "Eliminates unroutable checks and RTO stalls."],
            ["6. Windows Resets", "Win32 threw WSAECONNRESET (10054) on UDP ICMP unreachable.", "nat.IsTransientSocketError() traps 10054/10052 as non-fatal.", "Prevents socket closure during candidate hunting."],
            ["7. Output Flooding", "Runaway print loops in user scripts exhausted node memory.", "CappedBuffer truncates stdout/stderr at 3 KiB with truncated flag.", "Protects daemon memory from logging attacks."]
        ],
        col_widths=[1.3, 1.8, 2.0, 1.9]
    )

    add_h1("6. Comparative Systems Analysis", space_before=4)
    add_p("Objective 1 evaluated against relevant volunteer and distributed computing platforms:")

    create_compact_table(
        headers=["Metric / Dimension", "BOINC [1]", "Golem Network [3]", "Bacalhau / IPFS [18]", "UniCompute Objective 1"],
        rows=[
            ["Network Architecture", "Client-Server (HTTP Ingestion)", "P2P Overlay + Proxy Relays", "P2P (libp2p DHT)", "Mesh P2P + WSS Signaling Hub"],
            ["NAT Traversal Model", "None (Outbound Poll Only)", "Custom Relays", "libp2p AutoNAT / Relays", "Dual-Stack ICE + CGNAT Probe + DCUtR"],
            ["Carrier-Grade NAT Handling", "Unsupported", "Relay Fallback Only", "Circuit Relay Only", "Direct IPv6 Bypass + UNIR Blind Relay"],
            ["Data Transport Layer", "HTTP / 1.1 or HTTP / 2", "TCP / Libp2p Streams", "Bitswap over TCP/QUIC", "WebRTC SCTP (16 MiB Buffer)"],
            ["Local Storage Vault", "Ephemeral Scratchpad", "Temporary Host Folder", "Global IPFS Merkle DAG", "CAS Vault (Atomic Staging, Quotas)"],
            ["Execution Sandbox", "Unsandboxed Process", "Heavy Hypervisor VM (>1.8 GB)", "Server Docker Containers", "Air-Gapped Docker (--network none, 128 MB)"],
            ["Control Plane Autonomy", "Zero (Fails if Server Dies)", "Tied to Blockchain / Hub", "DHT Lookup Dependent", "Complete (Invariant CP-1 Proven)"]
        ],
        col_widths=[1.5, 1.3, 1.4, 1.4, 1.4]
    )

    add_h1("7. Key References & Technical Standards", space_before=4)
    refs = [
        ("1", "Anderson, D. P. (2004). \"BOINC: Public-Resource Computing and Storage.\" 5th IEEE/ACM GRID '04, pp. 4–10."),
        ("2", "Larson, S. M., et al. (2009). \"Folding@home: Distributed computing for biology.\" arXiv:0901.0866."),
        ("3", "Golem Factory GmbH. (2023). \"Golem Network Provider Runtime Architecture.\" Technical Documentation."),
        ("4", "APNIC Labs. (2025). \"IPv6 Deployment Measurements for India (IN).\" APNIC Reports."),
        ("5", "Weil, J., et al. (2012). \"IANA-Reserved IPv4 Prefix for Shared Address Space (Carrier-Grade NAT).\" IETF RFC 6598."),
        ("6", "Microsoft Corporation. (2025). \"Job Objects — Win32 Process and Thread Architecture.\" Microsoft Learn."),
        ("7", "Castro, M., & Liskov, B. (2002). \"Practical Byzantine Fault Tolerance.\" ACM TOCS, 20(4), pp. 398–461."),
        ("8", "Keranen, A., et al. (2018). \"Interactive Connectivity Establishment (ICE): NAT Traversal.\" IETF RFC 8445."),
        ("9", "Rosenberg, J., et al. (2020). \"Session Traversal Utilities for NAT (STUN).\" IETF RFC 8489."),
        ("10", "Mahy, R., et al. (2020). \"Traversal Using Relays around NAT (TURN).\" IETF RFC 8656."),
        ("11", "Audet, F., & Jennings, C. (2007). \"NAT Behavioral Requirements for Unicast UDP.\" IETF RFC 4787."),
        ("12", "Borg, J., et al. (2024). \"Syncthing Relay Protocol v1 Documentation.\" Syncthing Docs."),
        ("13", "Jesup, R., et al. (2021). \"WebRTC Data Channels.\" IETF RFC 8831."),
        ("14", "Stewart, R. (2007). \"Stream Control Transmission Protocol (SCTP).\" IETF RFC 4960."),
        ("15", "Mathis, M., et al. (1996). \"TCP Extensions for High Performance.\" IETF RFC 1323 / RFC 2018."),
        ("16", "Heo, T. (2025). \"Control Group v2 (cgroups v2) — Linux Kernel Guide.\" Linux Kernel Docs."),
        ("17", "Guha, S., & Wetherall, D. (2005). \"Empirical Study of End-to-End Communicability.\" ACM SIGCOMM IMC '05."),
        ("18", "Bacalhau Project / Protocol Labs. (2024). \"Compute-over-Data on IPFS.\" bacalhau.org.")
    ]

    for num, citation in refs:
        p_ref = doc.add_paragraph()
        p_ref.paragraph_format.left_indent = Inches(0.18)
        p_ref.paragraph_format.first_line_indent = Inches(-0.18)
        p_ref.paragraph_format.space_before = Pt(0)
        p_ref.paragraph_format.space_after = Pt(0.5)
        p_ref.paragraph_format.line_spacing = 1.02
        
        r_num = p_ref.add_run(f"[{num}] ")
        r_num.font.bold = True
        r_num.font.size = Pt(7.5)
        r_num.font.color.rgb = COLOR_PRIMARY
        
        r_cit = p_ref.add_run(citation)
        r_cit.font.size = Pt(7.5)
        r_cit.font.color.rgb = COLOR_DARK

    output_filename = "UniCompute_Objective1_Literature_Review_and_Analysis.docx"
    doc.save(output_filename)
    print("Exact 3-Page Document generated successfully.")

if __name__ == "__main__":
    generate_exact_3page_doc()
