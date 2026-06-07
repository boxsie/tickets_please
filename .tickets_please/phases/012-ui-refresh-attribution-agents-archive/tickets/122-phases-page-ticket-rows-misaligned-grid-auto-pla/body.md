Wave ticket rows on the phases/phase-detail pages were badly misaligned: titles shoved toward the centre with a big left gap.

Root cause: `.phase-wave-ticket` is a 3-col grid (`auto 1fr auto`) but the row rendered 4 children after the attribution Chip was added (#099): dot, title, badge, Chip. The 4th child auto-placed into an implicit 2nd-row column 1, which sized column 1 to the Chip's width and pushed the title right.

Fix: wrap badge + archived pill + chip in a single `.phase-wave-ticket-meta` flex cell so the row is a true 3 columns again.
