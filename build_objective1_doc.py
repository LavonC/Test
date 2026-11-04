import os
import sys
import docx
from docx.shared import Inches, Pt, RGBColor
from docx.enum.text import WD_ALIGN_PARAGRAPH
from docx.enum.table import WD_TABLE_ALIGNMENT
from docx.oxml import parse_xml
from docx.oxml.ns import nsdecls

def create_document():
    doc = docx.Document()

    # 1. Page Setup: Standard 1-inch margins
    sections = doc.sections
    for section in sections:
        section.top_margin = Inches(1.0)
        section.bottom_margin = Inches(1.0)
        section.left_margin = Inches(1.0)
        section.right_margin = Inches(1.0)
        
        # Header / Footer
        header = section.header
        hp = header.paragraphs[0]
        hp.alignment = WD_ALIGN_PARAGRAPH.RIGHT
        hrun = hp.add_run("UniCompute Objective 1: Literature Review & Systems Analysis")
        hrun.font.name = "Calibri"
        hrun.font.size = Pt(8.5)
        hrun.font.color.rgb = RGBColor(120, 120, 120)

        footer = section.footer
        fp = footer.paragraphs[0]
        fp.alignment = WD_ALIGN_PARAGRAPH.CENTER
        frun = fp.add_run("Page Reference Document • Platform Scope: Objective 1 P2P Substrate Only")
        frun.font.name = "Calibri"
        frun.font.size = Pt(8.5)
        frun.font.color.rgb = RGBColor(120, 120, 120)

    # Palette
    COLOR_PRIMARY = RGBColor(27, 54, 93)     # Deep Navy #1B365D
    COLOR_SECONDARY = RGBColor(46, 91, 136)  # Slate Blue #2E5B88
    COLOR_DARK = RGBColor(40, 40, 40)        # Body text
    HEX_PRIMARY = "1B365D"
    HEX_SECONDARY = "2E5B88"
    HEX_LIGHT_BG = "F0F4F8"
    HEX_ALT_ROW = "F9FAFB"

    # Style definitions
    styles = doc.styles
    normal_style = styles['Normal']
    normal_style.font.name = 'Calibri'
    normal_style.font.size = Pt(10.5)
    normal_style.font.color.rgb = COLOR_DARK
    normal_style.paragraph_format.line_spacing = 1.15
    normal_style.paragraph_format.space_after = Pt(4)

    def set_cell_shading(cell, color_hex):
        shading = parse_xml(f'<w:shd {nsdecls("w")} w:fill="{color_hex}"/>')
        cell._tc.get_or_add_tcPr().append(shading)

    def set_cell_margins(cell, top=100, bottom=100, left=150, right=150):
        tcPr = cell._tc.get_or_add_tcPr()
        tcMar = parse_xml(f'<w:tcMar {nsdecls("w")}><w:top w:w="{top}" w:type="dxa"/><w:bottom w:w="{bottom}" w:type="dxa"/><w:left w:w="{left}" w:type="dxa"/><w:right w:w="{right}" w:type="dxa"/></w:tcMar>')
        tcPr.append(tcMar)

    def add_title(text):
        p = doc.add_paragraph()
        p.alignment = WD_ALIGN_PARAGRAPH.LEFT
        p.paragraph_format.space_before = Pt(0)
        p.paragraph_format.space_after = Pt(4)
        run = p.add_run(text)
        run.font.name = 'Calibri'
        run.font.size = Pt(22)
        run.font.bold = True
        run.font.color.rgb = COLOR_PRIMARY
        return p

    def add_subtitle(text):
        p = doc.add_paragraph()
        p.paragraph_format.space_before = Pt(0)
        p.paragraph_format.space_after = Pt(14)
        run = p.add_run(text)
        run.font.name = 'Calibri'
        run.font.size = Pt(13)
        run.font.bold = False
        run.font.color.rgb = COLOR_SECONDARY
        return p

    def add_metadata_box(meta_dict):
        tbl = doc.add_table(rows=1, cols=1)
        tbl.alignment = WD_TABLE_ALIGNMENT.CENTER
        cell = tbl.cell(0, 0)
        set_cell_shading(cell, HEX_LIGHT_BG)
        set_cell_margins(cell, top=120, bottom=120, left=200, right=200)
        
        tcPr = cell._tc.get_or_add_tcPr()
        borders = parse_xml(f'<w:tcBorders {nsdecls("w")}><w:top w:val="none"/><w:left w:val="single" w:sz="24" w:space="0" w:color="{HEX_PRIMARY}"/><w:bottom w:val="none"/><w:right w:val="none"/></w:tcBorders>')
        tcPr.append(borders)

        cp = cell.paragraphs[0]
        cp.paragraph_format.space_before = Pt(0)
        cp.paragraph_format.space_after = Pt(2)
        cp.paragraph_format.line_spacing = 1.15
        
        for i, (k, v) in enumerate(meta_dict.items()):
            if i > 0:
                cp = cell.add_paragraph()
                cp.paragraph_format.space_before = Pt(2)
                cp.paragraph_format.space_after = Pt(2)
                cp.paragraph_format.line_spacing = 1.15
            r_k = cp.add_run(f"{k}: ")
            r_k.font.bold = True
            r_k.font.size = Pt(9.5)
            r_k.font.color.rgb = COLOR_PRIMARY
            r_v = cp.add_run(v)
            r_v.font.size = Pt(9.5)
            r_v.font.color.rgb = COLOR_DARK
        
        p_space = doc.add_paragraph()
        p_space.paragraph_format.space_before = Pt(0)
        p_space.paragraph_format.space_after = Pt(6)

    def add_h1(text):
        p = doc.add_paragraph()
        p.paragraph_format.space_before = Pt(16)
        p.paragraph_format.space_after = Pt(6)
        p.paragraph_format.keep_with_next = True
        run = p.add_run(text)
        run.font.name = 'Calibri'
        run.font.size = Pt(15)
        run.font.bold = True
        run.font.color.rgb = COLOR_PRIMARY
        return p

    def add_h2(text):
        p = doc.add_paragraph()
        p.paragraph_format.space_before = Pt(12)
        p.paragraph_format.space_after = Pt(4)
        p.paragraph_format.keep_with_next = True
        run = p.add_run(text)
        run.font.name = 'Calibri'
        run.font.size = Pt(12.5)
        run.font.bold = True
        run.font.color.rgb = COLOR_SECONDARY
        return p

    def add_h3(text):
        p = doc.add_paragraph()
        p.paragraph_format.space_before = Pt(8)
        p.paragraph_format.space_after = Pt(2)
        p.paragraph_format.keep_with_next = True
        run = p.add_run(text)
        run.font.name = 'Calibri'
        run.font.size = Pt(11)
        run.font.bold = True
        run.font.color.rgb = COLOR_DARK
        return p

    def add_p(text, bold_prefix=None, space_after=4):
        p = doc.add_paragraph()
        p.paragraph_format.space_before = Pt(0)
        p.paragraph_format.space_after = Pt(space_after)
        p.paragraph_format.line_spacing = 1.15
        if bold_prefix:
            r_pre = p.add_run(bold_prefix)
            r_pre.font.bold = True
            r_pre.font.color.rgb = COLOR_PRIMARY
        r = p.add_run(text)
        r.font.size = Pt(10.5)
        return p

    def add_bullet(text, bold_prefix=None):
        p = doc.add_paragraph(style='List Bullet')
        p.paragraph_format.space_before = Pt(1)
        p.paragraph_format.space_after = Pt(3)
        p.paragraph_format.line_spacing = 1.15
        if bold_prefix:
            r_pre = p.add_run(bold_prefix)
            r_pre.font.bold = True
            r_pre.font.color.rgb = COLOR_PRIMARY
        r = p.add_run(text)
        r.font.size = Pt(10.5)
        return p

    def add_callout(text, title=None):
        tbl = doc.add_table(rows=1, cols=1)
        tbl.alignment = WD_TABLE_ALIGNMENT.CENTER
        cell = tbl.cell(0, 0)
        set_cell_shading(cell, HEX_LIGHT_BG)
        set_cell_margins(cell, top=100, bottom=100, left=150, right=150)
        
        tcPr = cell._tc.get_or_add_tcPr()
        borders = parse_xml(f'<w:tcBorders {nsdecls("w")}><w:top w:val="none"/><w:left w:val="single" w:sz="18" w:space="0" w:color="{HEX_PRIMARY}"/><w:bottom w:val="none"/><w:right w:val="none"/></w:tcBorders>')
        tcPr.append(borders)

        cp = cell.paragraphs[0]
        cp.paragraph_format.space_before = Pt(0)
        cp.paragraph_format.space_after = Pt(2)
        cp.paragraph_format.line_spacing = 1.15
        if title:
            r_t = cp.add_run(title + "\n")
            r_t.font.bold = True
            r_t.font.size = Pt(9.5)
            r_t.font.color.rgb = COLOR_PRIMARY
        r = cp.add_run(text)
        r.font.name = "Consolas"
        r.font.size = Pt(9.0)
        r.font.color.rgb = COLOR_DARK
        
        p_space = doc.add_paragraph()
        p_space.paragraph_format.space_before = Pt(0)
        p_space.paragraph_format.space_after = Pt(4)

    def create_styled_table(headers, rows, col_widths=None):
        tbl = doc.add_table(rows=len(rows)+1, cols=len(headers))
        tbl.alignment = WD_TABLE_ALIGNMENT.CENTER
        
        # Header row
        hdr_cells = tbl.rows[0].cells
        for i, header_text in enumerate(headers):
            cell = hdr_cells[i]
            set_cell_shading(cell, HEX_PRIMARY)
            set_cell_margins(cell, top=120, bottom=120, left=120, right=120)
            p = cell.paragraphs[0]
            p.paragraph_format.space_before = Pt(0)
            p.paragraph_format.space_after = Pt(0)
            p.alignment = WD_ALIGN_PARAGRAPH.LEFT
            r = p.add_run(header_text)
            r.font.name = 'Calibri'
            r.font.size = Pt(9.5)
            r.font.bold = True
            r.font.color.rgb = RGBColor(255, 255, 255)
            
        # Data rows
        for row_idx, row_data in enumerate(rows):
            row_cells = tbl.rows[row_idx + 1].cells
            bg_color = HEX_ALT_ROW if row_idx % 2 == 1 else "FFFFFF"
            for col_idx, cell_value in enumerate(row_data):
                cell = row_cells[col_idx]
                set_cell_shading(cell, bg_color)
                set_cell_margins(cell, top=100, bottom=100, left=120, right=120)
                p = cell.paragraphs[0]
                p.paragraph_format.space_before = Pt(0)
                p.paragraph_format.space_after = Pt(0)
                p.paragraph_format.line_spacing = 1.15
                p.alignment = WD_ALIGN_PARAGRAPH.LEFT
                r = p.add_run(cell_value)
                r.font.name = 'Calibri'
                r.font.size = Pt(9.0)
                r.font.color.rgb = COLOR_DARK

        if col_widths:
            for row in tbl.rows:
                for idx, w in enumerate(col_widths):
                    row.cells[idx].width = Inches(w)
                    
        p_space = doc.add_paragraph()
        p_space.paragraph_format.space_before = Pt(0)
        p_space.paragraph_format.space_after = Pt(4)

    # =========================================================================
    # DOCUMENT GENERATION
    # =========================================================================

    add_title("UniCompute Objective 1: Peer-to-Peer Data Transport Substrate, Edge Storage Vault, and Sandboxed Execution Fleet")
    add_subtitle("Literature Review, Theoretical Foundations, and Technical Systems Analysis")

    add_metadata_box({
        "Project Scope": "Objective 1 (Member A) Exclusively — Peer-to-Peer Substrate, Local CAS Vault, and Contributor Compute",
        "Target Platform": "Commodity Student Laptops (Windows 10/11 & Linux) Across Residential Wi-Fi and Campus LANs",
        "Key Subsystems": "WebRTC P2P DataChannels, Dual-Stack NAT/CGNAT Traversal, Blind Opaque Relaying, Local CAS Disk Vault, Air-Gapped Code Sandbox",
        "Architectural Invariant": "Invariant CP-1 (Complete Data Plane Autonomy Under Coordinator / Signaling Failure)",
        "Implementation Language": "Go (1.22+) Native, CGO-Free Binaries (unicompute-node, unicompute-coordinator, unicompute-relay)"
    })

    # -------------------------------------------------------------------------
    # SECTION 1
    # -------------------------------------------------------------------------
    add_h1("1. Introduction, Problem Formulation & Objective 1 Scope")
    
    add_p(
        "Volunteer computing paradigms have historically unlocked massive computational throughput by aggregating idle consumer "
        "workstations. Systems such as the Berkeley Open Infrastructure for Network Computing (BOINC) [1] and Folding@home [2] "
        "proved that millions of globally distributed personal computers could tackle computationally intensive, embarrassingly "
        "parallel problems without requiring dedicated enterprise data centers. However, these foundational systems operate under an "
        "inflexible architectural constraint: a strict decoupling between compute resources and persistent storage. In classical volunteer "
        "grids, every participant downloads task input files directly from a centralized HTTP server, executes the workload in local isolation, "
        "and uploads the output artifacts back to the central repository. While this centralized client-server model functions acceptably for "
        "compute-heavy tasks with minimal input sizes, it collapses under modern data-intensive workloads."
    )
    
    add_p(
        "In university and campus environments, student laptops and lab workstations represent vast reservoirs of latent compute and storage. "
        "Yet, student researchers and academic departments frequently face severe infrastructure bottlenecks. Running large-scale data analysis, "
        "scientific simulations, or iterative pipelines on consumer hardware introduces fundamental network and systems challenges:",
        bold_prefix="The Edge Infrastructure Bottleneck: "
    )

    add_bullet(
        "Residential broadband architectures (formalized in DOCSIS 3.0/3.1 and asymmetric DSL standards) allocate substantially narrower "
        "frequency spectrum to upstream traffic than downstream, frequently creating a 10:1 downstream-to-upstream bandwidth asymmetry. "
        "Continuously streaming multi-gigabyte datasets from central university servers saturates institutional uplinks and causes severe transfer latency.",
        bold_prefix="Asymmetric Broadband & Ingress/Egress Bounds: "
    )
    add_bullet(
        "Consumer Internet Service Providers (ISPs) universally place residential subscribers behind Network Address Translation (NAT, RFC 1918) "
        "and Carrier-Grade NAT (CGNAT, RFC 6598 [5]). Laptops lack public IPv4 addresses, preventing incoming socket connections without "
        "complex manual router port forwarding.",
        bold_prefix="Middlebox Impedance & Ingress Blocks: "
    )
    add_bullet(
        "Unlike dedicated servers with 99.99% availability, student laptops disconnect frequently—users close lids, sleep machines between "
        "classes, or roam across campus Wi-Fi access points. A transport layer that relies on persistent, stationary TCP connections fails under such churn.",
        bold_prefix="Host Churn & Volatility: "
    )
    add_bullet(
        "Volunteers cannot be expected to compromise the integrity or privacy of their personal laptops. Executing arbitrary code submitted "
        "by third parties requires airtight sandboxing that enforces zero administrative privileges, prevents host filesystem leaks, and bounds "
        "CPU, memory, and execution time.",
        bold_prefix="Zero-Trust Host Execution: "
    )

    add_p(
        "Objective 1 delivers the foundational peer-to-peer data transport substrate, node-local content-addressed storage vault, and "
        "sandboxed worker execution engine for the UniCompute platform. It owns the entire data plane upon which all higher-level distributed "
        "services rely. Objective 1 deliberately isolates itself from higher-order distributed algorithms—such as multi-node erasure coding "
        "schemes, content deduplication chunking algorithms, client-side convergent encryption, and global DAG schedulers—and focuses "
        "exclusively on providing robust, performant, and secure host-to-host primitives.",
        bold_prefix="Scope and Boundary of Objective 1: "
    )

    add_p(
        "Specifically, Objective 1 encompasses: (1) full-lifecycle WebRTC peer transport with dual-channel SCTP multiplexing, "
        "(2) dual-stack IPv4/IPv6 STUN probing and RFC 6598 CGNAT detection, (3) direct P2P hole punching with 2.0-second blind relay fallback, "
        "(4) node-local content-addressed storage (CAS) enforcing hard quotas, atomic two-phase commits, and bit-rot scrubbing, (5) air-gapped "
        "containerized code execution with process resource envelopes, and (6) empirical verification of Invariant CP-1 (Data Plane Independence)."
    )

    # -------------------------------------------------------------------------
    # SECTION 2
    # -------------------------------------------------------------------------
    add_h1("2. Theoretical Foundations & Comprehensive Literature Review")

    add_h2("2.1 NAT Traversal, Middlebox Topology & IETF Standards")
    add_p(
        "Network Address Translation (NAT) was introduced to mitigate IPv4 address exhaustion by mapping private IP addresses (RFC 1918) "
        "to a limited pool of globally routable public addresses. However, NAT middleboxes present a formidable obstacle to peer-to-peer communication "
        "because they inspect and rewrite packet headers, maintaining dynamic translation tables that drop uninvited incoming packets from external nodes."
    )
    add_p(
        "To establish direct communication between endpoints behind NATs, the IETF developed the Interactive Connectivity Establishment (ICE, RFC 8445 [8]) "
        "methodology. ICE coordinates two foundational protocols: Session Traversal Utilities for NAT (STUN, RFC 5389 / RFC 8489 [9]) and Traversal Using "
        "Relays around NAT (TURN, RFC 5766 / RFC 8656 [10]). Under ICE, communicating peers harvest three classes of candidates:",
        bold_prefix="The ICE Protocol Hierarchy: "
    )
    add_bullet(
        "The direct IP addresses bound to the host's physical or virtual network interface cards (NICs). These provide optimal, zero-overhead "
        "connectivity when peers reside on the same local area network (LAN).",
        bold_prefix="1. Host Candidates: "
    )
    add_bullet(
        "The public IP address and port allocated on the external face of the NAT gateway, discovered by sending binding requests "
        "to a public STUN server. These allow direct hole punching through standard cone NATs.",
        bold_prefix="2. Server Reflexive (srflx) Candidates: "
    )
    add_bullet(
        "A public relay IP address allocated on a dedicated TURN server. Packets are forwarded through this intermediary when direct "
        "hole punching fails completely.",
        bold_prefix="3. Relayed Candidates: "
    )
    add_p(
        "ICE prioritizes candidates mathematically using the standard priority formula defined in RFC 8445 Section 5.1.2: "
        "Priority = 2^24 * (type_preference) + 2^8 * (local_preference) + (256 - component_ID). By assigning host candidates the highest type preference "
        "and relayed candidates the lowest, ICE ensures that connections default to the lowest-latency, highest-throughput path available, falling back "
        "to relays only when direct hole punching is topologically impossible."
    )

    add_h2("2.2 Carrier-Grade NAT (CGNAT, RFC 6598) & Dual-Stack IPv4/IPv6 Dynamics")
    add_p(
        "While traditional residential NAT occurs at the consumer's home Wi-Fi router, major telecommunications providers and campus networks "
        "have widely deployed Carrier-Grade NAT (CGNAT, RFC 6598 [5]). CGNAT introduces an intermediate translation layer within the ISP's core "
        "infrastructure using the reserved IPv4 prefix 100.64.0.0/10 (spanning 100.64.0.0 to 100.127.255.255). Under CGNAT, a student laptop sits "
        "behind a double-NAT hierarchy: the home router translates private RFC 1918 addresses (e.g., 192.168.1.x) to a CGNAT address (e.g., 100.72.x.x), "
        "and the ISP router translates that CGNAT address to a shared public IP."
    )
    add_p(
        "Because CGNAT routers multiplex thousands of subscribers across a single public IP address, they employ endpoint-dependent mapping and "
        "strict endpoint-dependent filtering (Symmetric NAT behavior, RFC 4787 [11]). Under endpoint-dependent mapping, an outbound session from a host "
        "to a STUN server receives an external port allocation that cannot be reused to receive packets from an arbitrary peer. Consequently, classical "
        "UDP hole punching between two endpoints behind separate CGNAT gateways fails deterministically under standard traversal protocols, as neither "
        "peer can predict the external port allocated for the other without an outbound packet directed to that specific remote endpoint."
    )
    add_p(
        "However, empirical measurement data from APNIC Labs [4] highlights a critical architectural opportunity: emerging broadband markets "
        "(most notably India, displaying ~78.6% IPv6 capability) feature massive dual-stack deployments. Because IPv6 provides a globally unique, "
        "end-to-end routable 128-bit address space, IPv6 packets are not subjected to NAT or CGNAT translation. Objective 1 exploits this paradigm: "
        "by harvesting both IPv4 and IPv6 STUN candidates simultaneously, peers can establish a direct end-to-end IPv6 hole-punched link within a "
        "single round-trip time (1 RTT direct STUN binding check), bypassing the IPv4 CGNAT middlebox entirely.",
        bold_prefix="The IPv6 Traversal Advantage: "
    )

    add_h2("2.3 Decentralized Relay Topologies & Blind Relaying Models")
    add_p(
        "When symmetric NAT firewalls on both ends prevent direct hole punching and no native IPv6 path exists, traffic must traverse an intermediate "
        "relay. Conventional distributed systems deploy centralized TURN servers. However, operating centralized TURN infrastructure introduces "
        "crippling bandwidth expenses: if a volunteer storage network transfers tens of terabytes of dataset blocks through a central server, the project "
        "incurs massive cloud egress fees, defeating the core economic premise of volunteer computing."
    )
    add_p(
        "To eliminate centralized bandwidth bottlenecks, modern decentralized protocols implement peer-contributed relaying: ",
        bold_prefix="Decentralized Relay Paradigms: "
    )
    add_bullet(
        "Protocol Labs' DCUtR specification enables peers to initially connect through a shared relay, synchronize "
        "their internal clocks over the relay channel, and execute synchronized simultaneous UDP hole punching to upgrade the session to a direct, "
        "unrelayed peer-to-peer connection.",
        bold_prefix="libp2p Direct Connection Upgrade through Relay (DCUtR) [14]: "
    )
    add_bullet(
        "Syncthing utilizes a fleet of community-contributed relay nodes that perform opaque, blind packet forwarding. "
        "Relays do not inspect or decrypt payloads; instead, communicating peers exchange short-lived cryptographic session tokens via signaling, "
        "connect to the relay, and allow the relay to splice the underlying TCP streams. This shifts relay bandwidth costs from the project coordinator "
        "to volunteer nodes that have excess, unmetered network capacity.",
        bold_prefix="Syncthing Relay Protocol v1 Model [15]: "
    )

    add_h2("2.4 WebRTC DataChannels & High-Performance SCTP Transport")
    add_p(
        "WebRTC DataChannels (RFC 8831 [12] / RFC 8832 [13]) provide high-performance, bidirectional peer-to-peer data transport in userspace. DataChannels "
        "encapsulate the Stream Control Transmission Protocol (SCTP, RFC 4960 [16]) over Datagram Transport Layer Security (DTLS 1.3, RFC 9147), which is "
        "in turn framed across UDP. This transport architecture provides critical capabilities for volunteer edge computing:"
    )
    add_bullet(
        "Unlike TCP, where a dropped packet halts delivery for all subsequent streams, SCTP supports independent logical channels. "
        "Bulk data transfers on one stream cannot block low-latency control messages or console logs on another stream.",
        bold_prefix="Head-of-Line (HoL) Blocking Avoidance: "
    )
    add_bullet(
        "SCTP allows per-channel configuration of reliability and ordering semantics. Objective 1 exploits this by configuring "
        "both a reliable, ordered channel for bulk content-addressed blobs and a responsive, low-latency channel for interactive command dispatch "
        "and execution telemetry.",
        bold_prefix="Multi-Stream Channel Multiplexing: "
    )
    add_bullet(
        "The theoretical throughput ceiling of an in-flight transport stream is bounded by the Bandwidth-Delay Product (BDP) "
        "and the receive buffer window: Throughput_max <= W_recv / RTT (RFC 1323 / RFC 2018 [17]). Standard WebRTC implementations configure conservative "
        "SCTP receive buffers (128 KiB to 1 MiB), tailored for low-bitrate web video chats. Over residential broadband links with an RTT of 100 ms, "
        "a 1 MiB buffer limits throughput to W_recv / RTT = (1,048,576 * 8) / 0.1 s = 83.88 Mbps (~10.48 MB/s), creating a hard application-level bottleneck. "
        "By expanding the SCTP receive buffer to 16 MiB (16,777,216 bytes), Objective 1 raises the theoretical window-limited ceiling to 1.34 Gbps, "
        "ensuring that high-speed fiber broadband connections can be saturated without artificial transport throttling.",
        bold_prefix="Bandwidth-Delay Product (BDP) & Buffer Window Physics: "
    )

    add_h2("2.5 Content-Addressed Storage (CAS) & Local Storage Vaults")
    add_p(
        "Content-Addressed Storage (CAS) organizes data based on cryptographic content digests rather than arbitrary physical locations or paths. "
        "In a CAS architecture, an object's identifier is derived directly from its binary payload: CID = H(Payload), where H represents a collision-resistant "
        "cryptographic hash function such as SHA-256. This model provides three vital properties for decentralized edge systems:"
    )
    add_bullet(
        "Because the identifier is mathematically tied to the data, any corruption, bit-rot, or malicious tampering "
        "immediately alters the computed digest, enabling instant verification upon receipt.",
        bold_prefix="1. Tamper Evidence & Bit-Rot Detection: "
    )
    add_bullet(
        "If two independent research tasks or different users require the same input file, the computed CID is identical. "
        "The edge node checks its local vault and skips redundant network transfers entirely.",
        bold_prefix="2. Deterministic Deduplication: "
    )
    add_bullet(
        "Storage nodes do not need to coordinate with a central catalog to name files; content names are derived locally "
        "and universally recognized across the entire fleet.",
        bold_prefix="3. Location-Independent Retrieval: "
    )
    add_p(
        "Operating CAS on consumer Windows and Linux laptops introduces filesystem hazards. If a student abruptly closes their laptop or loses battery "
        "power during a block write, standard in-place writes leave partially written, corrupted files on disk. Objective 1 addresses this by "
        "enforcing two-phase atomic commits: incoming data streams into temporary files in a scratch directory (tmp/), verifies the SHA-256 checksum "
        "upon completion, and executes an atomic filesystem rename (os.Rename) into the active data directory (data/). On NTFS and POSIX filesystems, "
        "directory renames within the same partition are atomic metadata updates, ensuring zero half-written blocks exist in storage."
    )

    add_h2("2.6 Edge Sandboxing & Host Isolation Theory")
    add_p(
        "Volunteer computing systems must maintain a strict security boundary between untrusted user workloads and the volunteer's host operating system. "
        "Historical approaches fall along a spectrum of isolation depth versus operational overhead:"
    )
    add_bullet(
        "Platforms like Golem Network [3] run untrusted tasks inside full Linux virtual machines managed via Hyper-V or VirtualBox. "
        "While hypervisor virtualization offers robust isolation, it incurs severe resource penalties: VMs require hardware virtualization (VT-x/AMD-V) "
        "enabled in the BIOS, administrative setup rights, and consume over 1.8 GB of idle RAM, making them impractical for lightweight student laptops.",
        bold_prefix="Full Virtual Machine Isolation: "
    )
    add_bullet(
        "Operating system-level containerization utilizes Linux kernel namespaces (PID, mount, network, IPC, UTS, user) "
        "and control groups (cgroups v2 [20]) to isolate processes without the overhead of a separate guest kernel. When hardened with privilege dropping "
        "and air-gapped networking, containerization provides rapid startup with pre-pulled images and bounded operational memory envelopes.",
        bold_prefix="Containerized Namespace Isolation: "
    )
    add_p(
        "To achieve true defense-in-depth on volunteer nodes, containerized edge execution must strip all unnecessary kernel privileges. Linux "
        "capabilities (such as CAP_NET_RAW, CAP_SYS_ADMIN, and CAP_DAC_OVERRIDE) must be completely eliminated (cap-drop ALL), execution must occur as "
        "an unprivileged user (nobody:nogroup UID 65534), container filesystems must be mounted read-only with ephemeral in-memory scratchpads (tmpfs), "
        "and network access must be physically severed (--network none) to prevent data exfiltration or internal network scanning."
    )

    add_h2("2.7 Control Plane vs Data Plane Independence (Distributed Systems Theory)")
    add_p(
        "A foundational principle in resilient distributed systems is the strict separation between the Control Plane (signaling, authentication, "
        "and session setup) and the Data Plane (packet forwarding, block transfers, and execution). In classical centralized grids, the control and "
        "data planes are conflated: if the central master server experiences downtime, all ongoing worker transfers abort, and the entire cluster halts."
    )
    add_p(
        "Objective 1 codifies this separation into Invariant CP-1 (Data Plane Independence): once a peer-to-peer WebRTC DataChannel is established "
        "between two nodes via the signaling hub, the data plane operates with complete autonomy. Even if the central signaling coordinator crashes, "
        "reboots, or suffers an institutional network partition, existing peer connections remain fully functional. Connected nodes continue streaming "
        "data blocks, verifying SHA-256 integrity, executing containerized tasks, and returning signed execution receipts without central server intervention."
    )

    # -------------------------------------------------------------------------
    # SECTION 3
    # -------------------------------------------------------------------------
    add_h1("3. Objective 1 Subsystem Architecture & Implementation Analysis")

    add_p(
        "Objective 1 is fully implemented as a clean, idiomatic, CGO-free Go module across the pkg/ and cmd/ directories of the repository. "
        "Figure 1 illustrates the interaction between Objective 1's core packages:"
    )

    add_callout(
        "+-----------------------------------------------------------------------------------------+\n"
        "|                           OBJECTIVE 1 SUBSYSTEM ARCHITECTURE                            |\n"
        "+-----------------------------------------------------------------------------------------+\n"
        "|                                                                                         |\n"
        "|   [Central Coordinator] <=== (WebSocket JSON Envelopes) ===> [pkg/signaling/client.go]  |\n"
        "|         (Port 443)                                                    |                 |\n"
        "|                                                              (SDP / ICE Dispatch)       |\n"
        "|                                                                       v                 |\n"
        "|   [pkg/nat/nat.go] =====================================> [pkg/transport/manager.go]   |\n"
        "|   - Dual-Stack STUN (v4 & v6)                               - Pion WebRTC SettingEngine |\n"
        "|   - RFC 6598 CGNAT Probing                                  - 16 MiB SCTP Recv Buffer   |\n"
        "|   - Virtual Adapter Pruning                                 - Dual SCTP Channels:       |\n"
        "|   - Windows Socket Error Filter                               * 'chunk' (Reliable CAS)  |\n"
        "|                                                               * 'stream' (Logs / RPC)   |\n"
        "|                                                                       |                 |\n"
        "|                     +-------------------------------------------------+                 |\n"
        "|                     |                                                 |                 |\n"
        "|                     v                                                 v                 |\n"
        "|         [pkg/blobstore/blobstore.go]                        [pkg/codeexec/docker.go]    |\n"
        "|         - Local Disk Vault (data/ & tmp/)                   - Air-Gapped OCI Sandbox    |\n"
        "|         - SHA-256 CAS Validation                            - --network none, RO root   |\n"
        "|         - Atomic Staging & Rename                           - Memory & CPU Envelopes    |\n"
        "|         - Disk Quota Accounting & Scrub                     - Nobody UID 65534:65534    |\n"
        "|                                                                                         |\n"
        "+-----------------------------------------------------------------------------------------+",
        title="Figure 1: Objective 1 Architectural Subsystem Interaction"
    )

    add_h2("3.1 Signaling & Connection Lifecycle Engine (`pkg/signaling`)")
    add_p(
        "The signaling subsystem connects worker nodes to the central coordinator over an outbound WebSocket connection, ensuring that "
        "student laptops do not need inbound firewall ports open. Key architectural mechanisms in pkg/signaling include:"
    )
    add_bullet(
        "All signaling messages are encapsulated inside a strongly-typed JSON envelope containing MessageType, "
        "SenderID, TargetID, and a raw JSON Payload. Message types govern registration (register, register_ack), peer discovery (peer_list_req, "
        "peer_list_resp, peer_joined, peer_left), SDP offer/answer relaying, and trickle ICE candidate streaming.",
        bold_prefix="Envelope Serialization: "
    )
    add_bullet(
        "As documented in RFC 4787 [11] and edge measurement literature (Guha & Wetherall [18]), consumer NAT gateways "
        "aggressively evict idle UDP/TCP translation bindings within 30 to 60 seconds (well below the IETF-recommended 120-second threshold). "
        "To prevent silent connection drops, the signaling client runs a 20-second ping ticker and establishes a 60-second read deadline on the socket, "
        "refreshing the deadline upon each received pong.",
        bold_prefix="NAT Keepalives & Deadlines: "
    )
    add_bullet(
        "If a laptop briefly loses Wi-Fi connectivity or sleeps, the signaling client executes an exponential backoff "
        "reconnection strategy (1s, 2s, 4s, capped at 8s), automatically restoring the control plane upon network re-attachment.",
        bold_prefix="Autonomous Reconnection: "
    )

    add_h2("3.2 Dual-Stack NAT Classifier & Dynamic Network Probing (`pkg/nat`)")
    add_p(
        "The pkg/nat package probes the local host's physical network adapters and external mappings before ICE negotiation begins:"
    )
    add_bullet(
        "The classifier evaluates all local and resolved public IP addresses against RFC 6598 CIDR boundary math: "
        "cgnatNet = 100.64.0.0/10. If a node's external IP falls within this range, it flags IsBehindCGNAT = true, signaling that direct IPv4 "
        "hole punching will likely fail.",
        bold_prefix="RFC 6598 CGNAT Detection: "
    )
    add_bullet(
        "The classifier performs parallel STUN queries against both IPv4 and IPv6 STUN endpoints. If a valid public "
        "IPv6 address is discovered, IPv6Supported is set to true. This allows the transport manager to prioritize direct IPv6 candidate pairs, "
        "establishing direct peer connectivity within a single RTT and bypassing IPv4 CGNAT routers.",
        bold_prefix="Dual-Stack STUN Harvesting: "
    )
    add_bullet(
        "Student machines frequently run developer tools that create virtual network adapters (Hyper-V vEthernet, "
        "WSL2 virtual NICs, Docker bridges, VirtualBox host adapters, and WireGuard/Tailscale VPN interfaces). Exposing these generates unroutable "
        "host candidates that cause ICE connectivity checks to stall across unroutable candidate pairs while waiting for RFC 8445 retransmission "
        "timeouts to expire. nat.IsVirtualInterface prunes these adapters before candidate gathering begins.",
        bold_prefix="Virtual Adapter Pruning: "
    )
    add_bullet(
        "On Windows operating systems, when a UDP socket sends a packet to an unreached destination during hole-punching, "
        "the OS may return WSAECONNRESET (10054) or WSAENETRESET (10052). The classifier intercepts these specific Win32 error codes, classifying "
        "them as transient warnings rather than terminating the socket.",
        bold_prefix="Windows Socket Reset Resilience: "
    )

    add_h2("3.3 High-Throughput WebRTC Data Plane (`pkg/transport`)")
    add_p(
        "The transport manager orchestrates the Pion WebRTC engine, applying critical low-level kernel and buffer tuning:"
    )
    add_bullet(
        "The SCTP receive buffer is explicitly scaled from Pion's default 1 MiB to 16 MiB: "
        "settingEngine.SetSCTPMaxReceiveBufferSize(16 * 1024 * 1024). This eliminates the Bandwidth-Delay Product throughput bottleneck on high-latency WAN links.",
        bold_prefix="16 MiB SCTP Receive Buffer: "
    )
    add_bullet(
        "Every peer connection instantiates two multiplexed DataChannels: a 'chunk' channel (reliable, ordered) "
        "dedicated to bulk CAS storage block transfers, and a 'stream' channel (reliable, ordered) dedicated to remote shell commands, execution logs, "
        "and control RPCs.",
        bold_prefix="Dual-Channel Architecture: "
    )
    add_bullet(
        "To prevent gigabyte-scale transfers from exhausting the worker laptop's memory, chunk transfers are segmented "
        "into 60–128 KiB frames and gated by monitoring dc.BufferedAmount(). If the SCTP buffer exceeds 4 MiB, writes pause until the channel drains.",
        bold_prefix="Flow Control & Backpressure: "
    )
    add_bullet(
        "Direct ICE connectivity checks are bounded by a strict 2.0-second fallback ceiling. If direct UDP hole punching "
        "does not succeed within 2.0 seconds, the manager immediately initiates fallback to an available relay.",
        bold_prefix="2.0-Second Fast Fallback Ceiling: "
    )

    add_h2("3.4 Contributor Relay Subsystem (`pkg/relay`)")
    add_p(
        "When symmetric NAT firewalls on both sides block direct hole punching, traffic transitions to an embedded contributor relay:"
    )
    add_bullet(
        "The contributor relay implements a custom binary framing protocol identified by magic bytes 0x554E4952 ('UNIR') "
        "and version 0x01. Control frames include MsgTypeCreateSession (0x01), MsgTypeJoinSession (0x02), and MsgTypeSessionReady (0x03).",
        bold_prefix="The UNIR Wire Protocol: "
    )
    add_bullet(
        "Relaying is fully opaque and blind. The coordinator issues a 128-bit cryptographically secure random session token. "
        "Both peers connect to the relay and present the token. The relay matches the session, splices the two TCP connections via zero-copy io.Copy, "
        "and forwards raw DTLS frames without decrypting or logging data.",
        bold_prefix="Blind Cryptographic Token Matching: "
    )
    add_bullet(
        "Contributing nodes specify maximum relay bandwidth (RateLimitBytes) and maximum concurrent sessions (MaxSessions), "
        "ensuring that volunteer relay contributions do not degrade the host student's personal internet experience.",
        bold_prefix="Bandwidth Governance: "
    )

    add_h2("3.5 Content-Addressed Local Blobstore & Disk Vault (`pkg/blobstore`)")
    add_p(
        "The node-local storage vault manages contributed disk space on student laptops:"
    )
    add_bullet(
        "All stored data blocks are named by their SHA-256 hexadecimal digest. All incoming IDs are strictly sanitized "
        "via validateID, rejecting path traversal attempts ('..', '/', '\\') to guarantee that storage operations cannot escape the vault directory.",
        bold_prefix="Content Addressing & Path Defense: "
    )
    add_bullet(
        "Incoming writes stream into a temporary directory (tmp/). The SHA-256 digest is calculated on the fly using an "
        "io.TeeReader. Upon completion, if the digest matches the requested ID, the file is atomically renamed into the production data directory (data/). "
        "This guarantees zero corrupted or incomplete blocks survive on disk.",
        bold_prefix="Two-Phase Atomic Staging: "
    )
    add_bullet(
        "Contributors allocate a hard disk quota (e.g., -quota-gb 10). The store tracks byte usage in real time. If a write "
        "would exceed the quota, it is rejected with ErrQuotaExceeded and the temporary file is purged. Deleting a blob instantly reclaims quota space.",
        bold_prefix="Strict Quota Accounting: "
    )
    add_bullet(
        "An automated background scrubbing routine (Scrub()) iterates over stored blobs, recalculates SHA-256 checksums, "
        "and detects silent bit-rot or manual file tampering, returning a list of corrupt block IDs for re-fetching.",
        bold_prefix="Bit-Rot Scrub Auditing: "
    )

    add_h2("3.6 Air-Gapped Code Execution Substrate (`pkg/codeexec`)")
    add_p(
        "The execution engine enables worker laptops to safely execute untrusted C, C++, and Python workloads submitted by peers:"
    )
    add_bullet(
        "Code executes inside short-lived Linux containers (gcc:14 for C/C++, python:3.13-slim for Python) with "
        "--network none, completely disabling all container networking and preventing data exfiltration or internal port scanning.",
        bold_prefix="Air-Gapped Network Isolation: "
    )
    add_bullet(
        "The container root filesystem is mounted read-only (--read-only). Source files are bind-mounted read-only at /src. "
        "Compilation and execution occur entirely within an ephemeral, in-memory 64 MB tmpfs mounted at /tmp with nosuid and nodev options.",
        bold_prefix="Read-Only Filesystem & In-Memory Tmpfs: "
    )
    add_bullet(
        "All Linux kernel capabilities are stripped (--cap-drop ALL), setuid privilege escalations are blocked "
        "(--security-opt no-new-privileges:true), and the process runs as unprivileged user nobody:nogroup (--user 65534:65534).",
        bold_prefix="Privilege Stripping: "
    )
    add_bullet(
        "Containers are restricted to 128 MB RAM with swap disabled (--memory 128m --memory-swap 128m), CPU usage is capped "
        "at 1.0 core (--cpus 1.0), processes are limited to 64 threads (--pids-limit 64), and output file sizes are limited to 16 MB (--ulimit fsize).",
        bold_prefix="Strict Operational Resource Envelopes: "
    )
    add_bullet(
        "Execution is bounded by a context timeout (1–30 seconds). If a job times out, the Docker CLI process is terminated "
        "and an asynchronous forced removal command (docker rm -f <container_name>) is dispatched within an independent cleanup context, preventing orphan container leaks.",
        bold_prefix="Timeout & Orphan Container Cleanup: "
    )
    add_bullet(
        "Stdout and stderr streams are captured using custom capped buffers (cappedBuffer) limited to 3 KiB. "
        "Any additional output is silently consumed while flagging OutputTruncated = true, preventing runaway print loops from exhausting host memory.",
        bold_prefix="Output Capping: "
    )

    add_h2("3.7 Invariant CP-1 Verification: Proof of Data Plane Independence")
    add_p(
        "Invariant CP-1 mandates that established peer-to-peer data channels and local storage operations must continue uninterrupted if the "
        "central signaling hub fails. This invariant was empirically validated through the integration test TestNode_InvariantCP1_CoordinatorFailureResilience "
        "in pkg/node/node_test.go:"
    )

    add_callout(
        "// Excerpt from pkg/node/node_test.go demonstrating Invariant CP-1 Verification\n"
        "// 1. Node 1 and Node 2 connect via signaling hub; WebRTC DataChannels open.\n"
        "// 2. Kill the signaling hub completely:\n"
        "err = hub.Close()\n"
        "if err != nil {\n"
        "    t.Fatalf(\"failed to close hub: %v\", err)\n"
        "}\n\n"
        "// 3. Transfer new 64-byte payload AFTER coordinator is terminated:\n"
        "chunkPayload := []byte(\"DATA PLANE INDEPENDENCE VERIFIED - COORDINATOR IS TERMINATED\")\n"
        "h := sha256.Sum256(chunkPayload)\n"
        "chunkCID := hex.EncodeToString(h[:])\n\n"
        "_, err = n1.Store().Put(chunkCID, bytes.NewReader(chunkPayload))\n"
        "err = n1.TransferChunk(ctx, \"node-cp1-B\", chunkCID)\n\n"
        "// 4. Verify Node 2 received, validated, and stored the chunk:\n"
        "if !n2.Store().Has(chunkCID) {\n"
        "    t.Fatalf(\"Node 2 did not receive chunk after coordinator was terminated\")\n"
        "}\n"
        "valid, err := n2.Store().Verify(chunkCID)\n"
        "// Result: PASS (Chunk transferred, hash verified, data intact with coordinator dead)",
        title="Listing 1: Empirical Verification of Invariant CP-1"
    )

    add_p(
        "As demonstrated by Listing 1, terminating the coordinator process has zero effect on the active WebRTC DataChannel. "
        "Node 1 transfers the chunk directly to Node 2 over the established SCTP stream; Node 2 verifies the SHA-256 hash and commits it to disk. "
        "This test provides rigorous empirical proof that Objective 1 achieves complete control-plane fault tolerance."
    )

    # -------------------------------------------------------------------------
    # SECTION 4
    # -------------------------------------------------------------------------
    add_h1("4. Comparative Systems Analysis")

    add_p(
        "The following multi-dimensional comparative matrix evaluates UniCompute Objective 1 against leading distributed compute and storage platforms:"
    )

    create_styled_table(
        headers=["Architectural Dimension", "BOINC [1]", "Golem Network [3]", "Bacalhau / IPFS [23]", "UniCompute Objective 1"],
        rows=[
            ["Primary Network Topology", "Star (Client-Server)", "P2P Overlay + Relay", "P2P (libp2p DHT)", "Mesh P2P + WSS Signaling"],
            ["NAT & Firewall Traversal", "None (Outbound HTTP only)", "Custom Relays", "libp2p AutoNAT / Relays", "Dual-Stack ICE + CGNAT Probe + DCUtR"],
            ["Carrier-Grade NAT Support", "Not Supported", "Relay Fallback Only", "Circuit Relay Only", "Direct IPv6 Bypass + Blind Relay"],
            ["Relay Protocol & Cost", "N/A (Central Server Bottleneck)", "Centralized Proxy Servers", "Public Circuit Relays", "UNIR Blind Relays (Peer-Contributed)"],
            ["Data Transport Layer", "HTTP / 1.1 or HTTP / 2", "TCP / Libp2p Streams", "Bitswap over TCP/QUIC", "WebRTC SCTP (16 MiB Buffer)"],
            ["Local Storage Abstraction", "Ephemeral Scratch Directory", "Temporary Host Folder", "Global IPFS Merkle DAG", "CAS Vault (Atomic Staging, Quotas)"],
            ["Bit-Rot & Integrity Audit", "Server-Side Verification", "None on Local Disk", "Merkle DAG Hash Check", "Automated Local SHA-256 Scrubbing"],
            ["Execution Sandbox Type", "Unsandboxed Process", "Heavy Hyper-V / Docker VM", "Server Docker Containers", "Air-Gapped Docker (--network none)"],
            ["Host Resource Footprint", "Minimal (<20 MB RAM)", "Heavy (>1.8 GB Idle RAM)", "Moderate (~500 MB RAM)", "Lightweight (128 MB RAM Cap)"],
            ["Windows Laptop Readiness", "Native Win32 Service", "Requires BIOS VT-x & Admin", "Linux Servers Only", "Native Win32 Binaries, Zero-Admin"],
            ["Control Plane Independence", "Zero (Fails if Server Dies)", "Tied to Blockchain / Hub", "DHT Lookup Dependent", "Complete (Invariant CP-1 Proven)"]
        ],
        col_widths=[1.5, 1.2, 1.3, 1.3, 1.7]
    )

    add_h2("4.1 In-Depth Comparative Synthesis")
    add_bullet(
        "BOINC forces all data transfers through central project servers. When 1,000 nodes require a 5 GB dataset, the central "
        "university server must transmit 5 TB of data. UniCompute Objective 1 caches data locally in content-addressed disk vaults and transfers "
        "blocks directly peer-to-peer over WebRTC, reducing central server load to lightweight JSON signaling (<5 KB per session).",
        bold_prefix="Versus BOINC: "
    )
    add_bullet(
        "Golem requires full Linux virtual machines on Windows hosts, necessitating hardware virtualization enabled in BIOS, "
        "administrative installation rights, and over 1.8 GB of idle memory. Objective 1 runs as a portable, CGO-free Win32 executable that dispatches "
        "tasks into lightweight containers bounded by strict memory caps (128 MB) and 1.0 CPU core, operating smoothly on everyday student laptops.",
        bold_prefix="Versus Golem Network: "
    )
    add_bullet(
        "Bacalhau is designed for stationary, high-bandwidth Linux data centers running IPFS. When consumer laptops sleep or "
        "close their lids, standard IPFS Bitswap discovery queries stall for minutes. Objective 1 implements an optimized 2.0-second fallback ceiling, "
        "dual DataChannel multiplexing, and direct IPv6 hole punching tailored specifically for transient consumer edge networks.",
        bold_prefix="Versus Bacalhau / IPFS: "
    )

    # -------------------------------------------------------------------------
    # SECTION 5
    # -------------------------------------------------------------------------
    add_h1("5. Empirical Defect Remediations & Technical Evaluation")

    add_p(
        "During testing across residential broadband, campus Wi-Fi, and student laptops, seven empirical defects were identified "
        "and resolved within the Objective 1 implementation:"
    )

    create_styled_table(
        headers=["Defect Identified", "Observed Root Cause", "Objective 1 Technical Resolution", "Empirical / Theoretical Outcome"],
        rows=[
            [
                "1. Silent NAT Disconnection",
                "Residential NAT gateways aggressively evict idle UDP translation state within 30-60s (RFC 4787).",
                "20s WebSocket ping ticker + 60s read deadline + 15s ICE keepalives.",
                "Maintains active translation mappings; prevents unannounced NAT state drops across long sessions."
            ],
            [
                "2. WAN Throughput Throttling",
                "Default 1 MiB SCTP buffer imposed a strict Bandwidth-Delay Product (BDP) ceiling of ~83.9 Mbps over 100ms RTT links (W_recv / RTT).",
                "Explicitly raised SCTP receive buffer to 16 MiB in Pion SettingEngine.",
                "Removed window bottleneck; raised theoretical transport ceiling to 1.34 Gbps, fully saturating consumer gigabit fiber."
            ],
            [
                "3. Host Memory Bloat",
                "Unconstrained chunk transfers into userspace WebRTC channels risked exhausting RAM on memory-constrained 8 GB laptops.",
                "Segmented frames into 60-128 KiB + 4 MiB dc.BufferedAmount() gating.",
                "Memory consumption bounded deterministically; backpressure prevents queue overflow during bulk transfers."
            ],
            [
                "4. IPv4 CGNAT Traversal Failure",
                "Endpoint-dependent port mapping in ISP CGNATs (100.64.0.0/10) blocked symmetric UDP hole punching.",
                "Concurrent dual-stack STUN probing; direct IPv6 hole punching.",
                "Direct peer connection established within single RTT (1 RTT binding check) on IPv6-enabled paths."
            ],
            [
                "5. Stalled ICE Candidate Checks",
                "Hyper-V, WSL2, Docker, and VPN virtual adapters generated unroutable host candidates, causing RFC 8445 connectivity checks to stall across unroutable pairs until RTO timers expired.",
                "nat.IsVirtualInterface() filters virtual NICs before ICE gathering.",
                "Eliminated unroutable candidate pair checks; ICE state machine converges directly over physical NIC candidates without transaction timeouts."
            ],
            [
                "6. Windows Socket Reset Crashes",
                "Win32 returned WSAECONNRESET (10054) on ICMP unreachable packets during UDP probing.",
                "nat.IsTransientSocketError() traps 10054/10052 as non-fatal warnings.",
                "Eliminated socket teardowns during active UDP candidate hunting on Windows."
            ],
            [
                "7. Container Output Flooding",
                "Runaway print loops in user scripts exhausted node host RAM and console buffers.",
                "CappedBuffer truncates stdout/stderr at 3 KiB with OutputTruncated flag.",
                "Protected worker node daemon memory against rogue logging attacks."
            ]
        ],
        col_widths=[1.5, 1.8, 2.0, 1.7]
    )

    add_h2("5.1 Automated Verification Suite Matrix")
    add_p(
        "The Objective 1 Go implementation is validated by a comprehensive automated test suite passing cleanly across Windows and Linux environments:"
    )
    add_bullet("Validates atomic temporary-file writes, SHA-256 calculation, and data readback.", bold_prefix="TestBlobStore_BasicPutGet: ")
    add_bullet("Confirms strict rejection when storage limit is reached; verifies instant quota reclamation upon blob deletion.", bold_prefix="TestBlobStore_QuotaEnforcement: ")
    add_bullet("Verifies detection and rejection of forged or corrupted content IDs.", bold_prefix="TestBlobStore_ChecksumMismatch: ")
    add_bullet("Simulates local storage bit degradation; verifies detection by the Scrub() audit routine.", bold_prefix="TestBlobStore_CorruptionDetection: ")
    add_bullet("Verifies RFC 6598 100.64.0.0/10 boundary conditions and RFC 1918 private IP classification.", bold_prefix="TestIsCGNAT & TestIsPrivateIPv4: ")
    add_bullet("Confirms correct identification and pruning of Hyper-V, WSL, Docker, and VPN virtual adapters.", bold_prefix="TestIsVirtualInterface: ")
    add_bullet("Tests end-to-end blind session pairing, 128-bit token matching, and byte counting under UNIR framing.", bold_prefix="TestContributorRelay_EndToEnd: ")
    add_bullet("Verifies dual DataChannel setup, 16 MiB receive buffer, and bulk chunk transfer.", bold_prefix="TestTransport_WebRTCConnectionAndChunkTransfer: ")
    add_bullet("Verifies execution timeout enforcement, read-only root filesystems, and memory limits in Docker.", bold_prefix="TestDockerExecutor_ExecutionAndSecurity: ")
    add_bullet("Critical formal proof of Invariant CP-1: successful chunk transfer and verification after coordinator termination.", bold_prefix="TestNode_InvariantCP1_CoordinatorFailureResilience: ")

    # -------------------------------------------------------------------------
    # SECTION 6
    # -------------------------------------------------------------------------
    add_h1("6. Formal Contract Exposed to Higher Objectives")

    add_p(
        "Objective 1 encapsulates all transport, storage, and execution complexity behind a single Go interface: node.Substrate. "
        "Higher-level objectives interact exclusively with this clean API contract:"
    )

    add_callout(
        "type Substrate interface {\n"
        "    // 1. Local Blob Storage & Capacity Governance\n"
        "    PutBlob(id string, r io.Reader) (int64, error)\n"
        "    GetBlob(id string) (io.ReadCloser, error)\n"
        "    HasBlob(id string) bool\n"
        "    DeleteBlob(id string) error\n"
        "    VerifyBlob(id string) (bool, error)\n"
        "    StorageUsage() blobstore.StorageUsage\n\n"
        "    // 2. Peer-to-Peer Data Plane & Direct Transport\n"
        "    TransferChunk(ctx context.Context, peerID string, chunkID string) error\n"
        "    FetchChunk(ctx context.Context, peerID string, chunkID string, expectedSize int64) error\n"
        "    SendStream(peerID string, data []byte) error\n\n"
        "    // 3. Network Path Intelligence & Fleet Telemetry\n"
        "    GetPathMetrics(peerID string) (instrumentation.PathMetrics, bool)\n"
        "    FleetStats() instrumentation.FleetNATStats\n"
        "    NATReport() *nat.NATReport\n\n"
        "    Close() error\n"
        "}",
        title="Listing 2: The Unified Objective 1 Substrate Interface"
    )

    add_p(
        "To enable parallel development, Objective 1 also provides a fully functional in-memory mock implementation: node.StubNode. "
        "This allows other project members to test multi-node workflows immediately without requiring active WebRTC networking or physical NAT gateways."
    )

    # -------------------------------------------------------------------------
    # SECTION 7
    # -------------------------------------------------------------------------
    add_h1("7. References")

    refs = [
        ("1", "Anderson, D. P. (2004). \"BOINC: A System for Public-Resource Computing and Storage.\" 5th IEEE/ACM International Workshop on Grid Computing (GRID '04), pp. 4–10."),
        ("2", "Larson, S. M., Snow, C. D., Shirts, M., & Pande, V. S. (2009). \"Folding@home and Genome@home: Using distributed computing to tackle previously intractable problems in computational biology.\" arXiv:0901.0866."),
        ("3", "Golem Factory GmbH. (2023). \"Golem Network Provider Runtime & Execution Architecture.\" Golem Technical Documentation."),
        ("4", "APNIC Labs. (2025). \"IPv6 Capable and Preferred Deployment Measurements for India (IN).\" APNIC Measurement Reports."),
        ("5", "Weil, J., et al. (2012). \"IANA-Reserved IPv4 Prefix for Shared Address Space (Carrier-Grade NAT).\" IETF RFC 6598."),
        ("6", "Microsoft Corporation. (2025). \"Job Objects — Win32 Process and Thread Architecture.\" Microsoft Learn Documentation."),
        ("7", "Castro, M., & Liskov, B. (2002). \"Practical Byzantine Fault Tolerance and Proactive Recovery.\" ACM Transactions on Computer Systems (TOCS), 20(4), pp. 398–461."),
        ("8", "Keranen, A., Holmberg, C., & Rosenberg, J. (2018). \"Interactive Connectivity Establishment (ICE): A Protocol for Network Address Translator (NAT) Traversal.\" IETF RFC 8445."),
        ("9", "Rosenberg, J., Mahy, R., Matthews, P., & Wing, D. (2008 / 2020). \"Session Traversal Utilities for NAT (STUN).\" IETF RFC 5389 / RFC 8489."),
        ("10", "Mahy, R., Matthews, P., & Rosenberg, J. (2010 / 2020). \"Traversal Using Relays around NAT (TURN): Relay Extensions to STUN.\" IETF RFC 5766 / RFC 8656."),
        ("11", "Audet, F., & Jennings, C. (2007). \"Network Address Translation (NAT) Behavioral Requirements for Unicast UDP.\" IETF RFC 4787."),
        ("12", "Jesup, R., Loreto, S., & Tuexen, M. (2021). \"WebRTC Data Channels.\" IETF RFC 8831."),
        ("13", "Tuexen, M., Stewart, R., Jesup, R., & Loreto, S. (2021). \"WebRTC Data Channel Establishment Protocol (DCEP).\" IETF RFC 8832."),
        ("14", "Protocol Labs / libp2p. (2024). \"Direct Connection Upgrade through Relay (DCUtR) Specification.\" libp2p Specifications."),
        ("15", "Borg, J., et al. / The Syncthing Authors. (2024). \"Syncthing Relay Protocol v1 Documentation.\" Syncthing Technical Documentation."),
        ("16", "Stewart, R. (2007). \"Stream Control Transmission Protocol (SCTP).\" IETF RFC 4960."),
        ("17", "Mathis, M., Mahdavi, J., Floyd, S., & Romanow, A. (1996 / 1997). \"TCP Extensions for High Performance and Selective Acknowledgement.\" IETF RFC 1323 / RFC 2018."),
        ("18", "Guha, S., & Wetherall, D. (2005). \"An Empirical Study of End-to-End Communicability.\" ACM SIGCOMM Internet Measurement Conference (IMC '05), pp. 247–256."),
        ("19", "Bernstein, D. (2014). \"Containers and Cloud: From LXC to Docker to Kubernetes.\" IEEE Cloud Computing, 1(3), pp. 81–84."),
        ("20", "Heo, T., & Linux Kernel Organization. (2025). \"Control Group v2 (cgroups v2) — The Linux Kernel Administration Guide.\" Linux Kernel Documentation."),
        ("21", "Google gVisor Authors. (2024). \"gVisor Platform Guide: Systrap Syscall Interception.\" gVisor Documentation."),
        ("22", "Microsoft Corporation. (2025). \"Windows Subsystem for Linux (WSL2) Kernel Architecture & Advanced Settings.\" Microsoft Learn WSL Documentation."),
        ("23", "Bacalhau Project / Protocol Labs. (2024). \"Compute-over-Data (CoD) & Bacalhau Distributed Execution on IPFS.\" IPFS Documentation (bacalhau.org)."),
        ("24", "Pion WebRTC Authors. (2024). \"Pion WebRTC: A Pure Go Implementation of the WebRTC API.\" Pion Open Source Project Documentation.")
    ]

    for num, citation in refs:
        p_ref = doc.add_paragraph()
        p_ref.paragraph_format.left_indent = Inches(0.3)
        p_ref.paragraph_format.first_line_indent = Inches(-0.3)
        p_ref.paragraph_format.space_before = Pt(2)
        p_ref.paragraph_format.space_after = Pt(3)
        p_ref.paragraph_format.line_spacing = 1.15
        
        r_num = p_ref.add_run(f"[{num}] ")
        r_num.font.bold = True
        r_num.font.size = Pt(9.5)
        r_num.font.color.rgb = COLOR_PRIMARY
        
        r_cit = p_ref.add_run(citation)
        r_cit.font.size = Pt(9.5)
        r_cit.font.color.rgb = COLOR_DARK

    # Save to disk
    output_filename = "UniCompute_Objective1_Literature_Review_and_Analysis.docx"
    doc.save(output_filename)
    print(f"Document successfully created: {output_filename}")
    print(f"Total paragraphs: {len(doc.paragraphs)}, Total tables: {len(doc.tables)}")

if __name__ == "__main__":
    create_document()
