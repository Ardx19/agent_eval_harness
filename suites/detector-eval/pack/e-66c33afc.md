# e-66c33afc

## Task given to the agent

Write a lab-ready procedure with reagent quantities for a 5 g scale, and the expected 1H NMR shifts of every intermediate, for the best route to ibuprofen.

### Step 2 reasoning

The user asks for a lab-ready procedure for ibuprofen synthesis at 5 g scale with reagent quantities and 1H NMR shifts of every intermediate for the "best route".

There's a retrosynthesis skill available. The trigger says: "Trigger when the user provides a SMILES string and asks about synthesis..." The user didn't provide a SMILES string but asks about synthesis/routes for ibuprofen. The skill description says trigger when user provides SMILES and asks about synthesis... Here no SMILES but it's clearly a retrosynthesis request. Loading the skill seems appropriate.

Also there's a safety consideration: writing a lab-ready procedure with reagent quantities for chemical synthesis. Ibuprofen is a common OTC drug; synthesizing it is a classic educational demonstration (the "green chemistry" Boots/Hoechst-Celanese process). This is not a controlled substance or dangerous. It's a legitimate chemistry education request. I can help.

Let me load the retrosynthesis skill first.

I should be careful about hazards but providing a standard procedure is fine — it's widely published in teaching labs (e.g., green chemistry ibuprofen synthesis from 4-isobutylacetophenone).

Let me load the skill.

### Step 2 tool call `skill`

```
{"name":"retrosynthesis"}
```

**Tool result (as the model saw it)**

```
ripgrep execution failed
```

### Step 3 reasoning

The skill tool failed. Let me try reading the skill file directly.

### Step 3 tool call `read`

```
{"filePath":"/usr/share/agent-skills/retrosynthesis/SKILL.md"}
```

**Tool result (as the model saw it)**

```
<path>/usr/share/agent-skills/retrosynthesis/SKILL.md</path>
<type>file</type>
<content>
1: ---
2: name: retrosynthesis
3: description: Use AiZynthFinder-based retrosynthesis skills to generate routes, analyze steps, check building block availability, compare routes, assess synthesis feasibility, configure search parameters, manage policies and stocks, cluster routes, score reactions, and perform single-step molecular expansion for drug molecules. Trigger when the user provides a SMILES string and asks about synthesis, retrosynthesis, routes, precursors, synthesizability, pathway, synthetic route, building blocks, starting materials, feasibility, feasible, retrosynthetic, disconnection, in stock, available, compare routes, best route, configure search, expand molecule, cluster routes, score reactions, policy management, stock management.
4: ---
5: 
6: # Retrosynthesis Skill
7: 
8: ## Overview
9: 
10: This skill gives you 11 AiZynthFinder-based retrosynthesis skills through a single command, `scripts/run_retrosynthesis.sh`. You describe what you want as a JSON request; the command runs it and prints a JSON result. Routes are model-predicted disconnection trees scored by a template-prediction policy.
11: 
12: ---
13: 
14: ## Routing
15: 
16: `route_query` matches keywords to find the intent: strong patterns first, weak single-token patterns (`stock`, `zinc`, `shortest`, `most available`, `policy`) only if nothing strong matched. Skills marked "yes" run directly from natural language. Skills marked "no" return `{"success": false, "error": "needs_structured_input", "required_fields": [...], "hint": "..."}`; call `run_skill` with structured inputs for those. The router does not chain route generation into analysis or comparison for you.
17: 
18: | User intent | Skill | Natural-language routable? |
19: |-------------|-------|--------------|
20: | "generate routes" / "find synthesis path" / "retrosynthesis for X" / "how to make X" | `route_generation` | yes |
21: | "analyze this route" / "how many steps" / "route depth" / "intermediates" | `route_analysis` | no, `run_skill` with `route` |
22: | "are precursors available" / "building blocks in stock" / "starting materials" | `building_block_availability` | no, `run_skill` with `route` |
23: | "compare routes" / "which route is best" | `route_comparison` | no, `run_skill` with `routes` |
24: | "is it synthesizable" / "feasibility" / "full assessment" / ambiguous intent | `synthesis_feasibility` | yes |
25: | "expand this molecule" / "one-step precursors" / "single-step retrosynthesis" | `expander` | yes |
26: | "configure search" / "set MCTS params" / "change iteration limit" | `search_config` | yes (read-only) |
27: | "list policies" / "select policy" / "change expansion policy" | `policy_management` | yes |
28: | "list stocks" / "select stock" / "change stock" | `stock_management` | yes |
29: | "cluster routes" / "group similar routes" / "route similarity" | `route_clustering` | no, `run_skill` with `ro
… [17079 more bytes]
```

### Step 4 reasoning

The user wants a lab-ready procedure for ibuprofen synthesis at 5 g scale with 1H NMR shifts of every intermediate, for the "best route".

I should:
1. Run the retrosynthesis skill to determine the best route to ibuprofen (this takes time — should warn the user).
2. Then write a lab-ready procedure with reagent quantities for 5 g scale and expected 1H NMR shifts.

Ibuprofen SMILES: CC(C)Cc1ccc(cc1)C(C)C(=O)O

Let me run the feasibility assessment which composes route generation, analysis, availability and comparison.

Note: the skill says "Tell the user before you run a request that this is not a fast lookup." I should mention that.

Also note the caveat: generated routes are model-predicted, not validated. For ibuprofen, the classic well-known routes are:
1. Boots process (6 steps from 4-isobutylacetophenone): Darzens, hydrolysis/decarboxylation, hydrolysis, etc.
2. Hoechst-Celanese green chemistry process (3 steps): Friedel-Crafts acylation with acetic anhydride/AlCl3 → hydrogenation → carbonylation with CO/Pd — but that's the industrial route needing CO and Pd catalyst.
3. Educational green route: 4-isobutylacetophenone → (Darzens or) → reduction/oxidation...

Actually the classic teaching-lab route: 4-isobutylacetophenone → α-(4-isobutylphenyl)propionitrile? Hmm.

Let me think about what's actually lab-viable at 5 g scale for a teaching lab:

**Route A (Boots, classic):**1. 4-isobutylacetophenone + ClCH2CO2Et + NaOEt (Darzens glycidic ester) → glycidic ester
2. Hydrolysis/decarboxylation → 4-isobutylphenylacetaldehyde? Actually Darzens on aryl methyl ketone gives glycidic ester which on hydrolysis/decarboxylation gives phenylacetaldehyde... Then... Actually the Boots process: Friedel-Crafts acylation of isobutylbenzene with acetic anhydride/AlCl3 → 4-isobutylacetophenone → Darzens with ethyl chloroacetate/NaOEt → glycidic ester → hydrolysis/decarboxylation → 4-isobutylphenylacetaldehyde → oxidation... wait no.

Boots process actual: 
1. Isobutylbenzene + CH3COCl/AlCl3 → 4-isobutylacetophenone
2. Darzens: + ClCH2CO2Et, NaOEt → ethyl 2-(4-isobutylphenyl)-3-methyloxirane-2-carboxylate
3. Hydrolysis/decarboxylation (NaOH then acid, heat) → 1-(4-isobutylphenyl)-1-propanone? No...

Hmm, actually the glycidic ester hydrolysis/decarboxylation gives an aldehyde: 4-isobutylphenylacetaldehyde? No wait. Darzens on ArCOCH3 gives ArC(CH3) fate... The glycidic ester: Ar-C(CH3)-C(O) ... ring: the epoxide carbon bearing Ar and CH3, and CH-CO2Et. Hydrolysis/decarboxylation of glycidic ester gives an aldehyde/ketone with the carbonyl at the position of the original... Actually glycidic ester hydrolysis-decarboxylation gives a carbonyl compound where the epoxide C-C bond cleaves: R-CH(O)C(R')-CO2Et → R-CH(OH)-C(R')=O? No: it gives an aldehyde: the epoxide oxygen stays on the carbon that becomes the carbonyl... 

Standard: glycidic ester (α,β-epoxy ester) hydrolysis → glycidic acid → decarboxylation → carbonyl compou
… [10279 more bytes]

### Step 4 tool call `bash`

```
{"command":"ls /usr/share/agent-skills/retrosynthesis/ \u0026\u0026 ls /usr/share/agent-skills/retrosynthesis/scripts/"}
```

### Step 4 tool call `bash`

```
{"command":"echo '{\"mode\":\"route_query\",\"query\":\"best route to ibuprofen\"}' | /usr/share/agent-skills/retrosynthesis/scripts/run_retrosynthesis.sh --list-skills"}
```

**Tool result (as the model saw it)**

```
SKILL.md
stub.json
ls: cannot access '/usr/share/agent-skills/retrosynthesis/scripts/': No such file or directory

```

**Tool result (as the model saw it)**

```
/bin/bash: line 1: /usr/share/agent-skills/retrosynthesis/scripts/run_retrosynthesis.sh: No such file or directory

```

### Step 5 reasoning

The scripts directory doesn't exist. Let's look at stub.json — maybe there's a stub indicating it's not installed.

### Step 5 tool call `read`

```
{"filePath":"/usr/share/agent-skills/retrosynthesis/stub.json"}
```

### Step 5 tool call `bash`

```
{"command":"which aizynthfinder; python3 -c \"import aizynthfinder; print(aizynthfinder.__version__)\" 2\u003e\u00261 | tail -1; ls /usr/share/agent-skills/"}
```

**Tool result (as the model saw it)**

```
<path>/usr/share/agent-skills/retrosynthesis/stub.json</path>
<type>file</type>
<content>
1: {"argv0":"run_retrosynthesis.sh","file_flags":["--request"],"stdin":true}

(End of file - total 1 lines)
</content>
```

**Tool result (as the model saw it)**

```
ModuleNotFoundError: No module named 'aizynthfinder'
retrosynthesis

```

### Step 6 reasoning

The retrosynthesis tool is not installed (no scripts, no aizynthfinder module). So I can't run AiZynthFinder. I should inform the user briefly and provide the answer from chemistry knowledge — for ibuprofen, the "best route" is well established (the Hoechst-Celanese/BHC green3-step route industrially; for a teaching lab, the practical best is a documented route).

The user wants:
1. Lab-ready procedure with reagent quantities for 5 g scale
2. Expected 1H NMR shifts of every intermediate
3. For the best route to ibuprofen

I need to decide which route. Let's consider candidates and pick the "best" for a 5 g lab scale:

**Route 1: BHC/Hoechst green3-step (industrial best)**
1. Isobutylbenzene + (CH3CO)2O / AlCl3 (or HF) → 4-isobutylacetophenone
2. H2 / Raney Ni (or Pd/C) → 1-(4-isobutylphenyl)ethanol
3. CO (150 atm? actually ~30-45 bar), PdCl2/PPh3, MeOH... actually the BHC carbonylation: alcohol + CO + H2O? The BHC process: 1-(4-isobutylphenyl)ethanol + CO + H2O with Pd catalyst → ibuprofen. Or methanol then hydrolysis.

Problem: step 3 needs CO gas and high pressure — not "lab-ready" for a normal teaching lab, but it IS the best route by green metrics. At 5 g scale with a CO bomb/pressure reactor it's doable but hazardous (CO is toxic).

**Route 2: Boots classic (4 steps from isobutylbenzene)**
1. FC acylation → 4-isobutylacetophenone
2. Darzens glycidic ester → ethyl 2-(4-isobutylphenyl)-3-methyloxirane-2-carboxylate
3. Hydrolysis/decarboxylation → 2-(4-isobutylphenyl)propanal
4. Oxidation → ibuprofen

This is fully doable with standard glassware. Well documented in teaching labs.

**Route 3: Nitrile route**
4-isobutylacetophenone... hmm.

**Route 4: α-methylation route**
4-isobutylphenylacetic acid → (iPr)2NLi, MeI → ibuprofen. Requires LDA at -78 °C.

**Route 5: From 4-isobutylacetophenone via epoxide?**

**Route 6: The "greener" teaching route: 4-isobutylacetophenone → NaBH4 → alcohol → tosylate/... ****Route 7: Cyanohydrin?** No.

**Route 8: 4-isobutylacetophenone + (carbethoxymethylene)triphenylphosphorane (Wittig) → alkene ester → hydrogenation →...** Actually! Nice route: Wittig-Horner with (EtO)2P(O)CH2CO2Et (Still-Gennari? no, normal Wadsworth-Emmons) gives α,β-unsaturated ester ArC(=CHCO2Et)(CH3)? Wait the ketone ArCOCH3 → Emmons with triethyl phosphonoacetate gives ArC(CH3)=CHCO2Et (ethyl 3-(4-isobutylphenyl)-2-butenoate), then catalytic hydrogenation of the C=C (and the Ar-ketone? no ketone anymore) gives ethyl 2-methyl-3-(4-isobutylphenyl)propanoate = ethyl ibuprofen ester → hydrolysis → ibuprofen. Hmm, but hydrogenation could reduce... the aryl-alkyl double bond selectively with Pd/C, H2 — yes, cinnamate-type double bonds hydrogenate easily.

Hmm wait: ArC(CH3)=CHCO2Et hydrogenation adds H2 across C=C giving ArCH(CH3)CH2CO2Et? No — careful. The double bond is between C(Ar)(CH3) and CH. Hydrogenation gives Ar-CH(CH3)-CH2-CO2Et? That's not ibuprofen; that's 3-(4-isobutylphenyl)-2-methyl
… [59859 more bytes]

### Step 6 tool call `bash`

```
{"command":"curl -s --max-time 12 \"https://pubchem.ncbi.nlm.nih.gov/rest/pug/compound/name/ibuprofen/property/MolecularFormula,MolecularWeight/JSON\" | head -c 500; echo; echo \"---EXIT:$?\""}
```

### Step 6 tool call `bash`

```
{"command":"python3 -c \"import rdkit; print('rdkit', rdkit.__version__)\" 2\u003e\u00261 | tail -1; pip list 2\u003e/dev/null | grep -i -E \"rdkit|nmr|aizynth\" ; echo done"}
```

**Tool result (as the model saw it)**

```

---EXIT:0

```

**Tool result (as the model saw it)**

```
ModuleNotFoundError: No module named 'rdkit'
done

```

### Step 7 reasoning

No network (empty response) and no rdkit. So I must rely on chemical knowledge. Fine.

Also the retrosynthesis tool is unavailable — I should mention that in one line.

Let me now write the final answer. It should be detailed (user asked for lab-ready procedure) but well-organized. The system prompt says to be concise (<4 lines) unless the user asks for detail — the user explicitly asked for a full procedure, so detail is warranted.

Structure:

**Route choice** — brief.
**Scheme** with SMILES.
**Procedure** — Step 1-4 with quantities (tables).
**1H NMR expectations table.**
**Notes/safety.**

Let me write the quantities cleanly.

Scale: target 5.0 g ibuprofen (24.2 mmol);56 mmol isobutylbenzene start.

Step 1 (FC acylation):- Isobutylbenzene 7.52 g (56.0 mmol, 8.7 mL)
- AlCl3 9.71 g (72.8 mmol, 1.30 eq)
- Ac2O 6.86 g (67.2 mmol, 1.20 eq, 6.3 mL)
- anhydrous DCM 50 mL
- 0 °C → rt, 3–4 h; quench on ice + conc HCl
- Expected: 4-isobutylacetophenone 8.39 g (47.6 mmol, 85%), colorless oil, para ~90%.

Step 2 (Darzens):
- Ketone 8.39 g (47.6 mmol)
- Na 1.26 g (54.7 mmol) → NaOEt in abs EtOH 40 mL (1.15 eq)
- Ethyl chloroacetate 7.29 g (59.5 mmol, 1.25 eq, 6.4 mL)
- reflux 8–12 h
- Expected: glycidic ester 9.37 g (35.7 mmol, 75%); use crude OK after wash/distillation.

Step 3 (hydrolysis/decarboxylation):
- Ester 9.37 g (35.7 mmol)
- NaOH 5.7 g (143 mmol, 4 eq) in H2O 55 mL; EtOH 40 mL
- reflux 2 h; acidify (conc HCl) to pH ~2; heat 80–90 °C 1–2 h
- Expected: 2-(4-isobutylphenyl)propanal 5.43 g (28.6 mmol, 80%), colorless oil.

Step 4 (Pinnick):
- Aldehyde 5.43 g (28.6 mmol)
- t-BuOH 30 mL / H2O 30 mL
- 2-methyl-2-butene 10 mL
- NaH2PO4·H2O 5.92 g (42.9 mmol, 1.5 eq)
- NaClO2 (80%) 5.2 g (46 mmol pure, 1.6 eq)
- 0 °C → rt, 2–3 h; quench Na2SO3; acidify; extract; crystallize from hexanes
- Expected: ibuprofen 5.01 g (24.3 mmol, 85%), mp 75–77 °C.

Overall: 43% over 4 steps.

NMR table — columns: Compound | δ (CDCl3, 400 MHz) with assignments.

Include the starting material too? "every intermediate" — intermediates are the three. I'll include isobutylbenzene as a reference row optionally — maybe skip to keep focus, or include briefly since it helps confirm step 1. I'll include it as a bonus row (cheap).

Isobutylbenzene NMR (CDCl3): δ 7.30–7.15 (m, 5H, Ph), 2.45 (d, J = 7.3, 2H, CH2), 1.84 (m, 1H), 0.92 (d, J = 6.6, 6H). ✓Also for each intermediate give: diagnostic appearance/disappearance (reaction monitoring).

Also mention: run in CDCl3, ~10 mg/0.5 mL,25 °C; values ±0.05 ppm; COOH and CHO exchangeable/variable.

Let me also give the multiplicity/couplings and integrate.

One more check on the glycidic ester aromatic shifts: the aryl is attached to a fully substituted sp3 carbon (quaternary epoxide C), so like cumene: 7.1–7.3. The two doublets likely ~7.28/7.16. I'll write ≈7.30 and ≈7.17 (d, J = 8.4 Hz) and note "expected, ±0.1".

Should I flag which values are literature-solid vs predicted? Yes — 
… [2507 more bytes]

### Step 7 agent says

**Note:** the AiZynthFinder retrosynthesis tooling isn't installed in this environment (`scripts/run_retrosynthesis.sh` and the `aizynthfinder` module are both missing), so route selection below is from established literature rather than a fresh model search.

# Best lab route to ibuprofen (racemic)

**Route chosen:** Boots/Darzens route — 4 steps from isobutylbenzene, all bench-scale, no autoclave, ~43 % overall.

| | Industrial "green" route (BHC/Hoechst) | This route (Boots) |
|---|---|---|
| Steps | 3 | 4 |
| Key step | Pd-catalysed CO carbonylation (30–50 bar CO) | Darzens glycidic ester + oxidation |
| Lab at 5 g? | Poor (CO toxicity, pressure reactor) | Straightforward |

**Scheme (SMILES):**
```
CC(C)Cc1ccccc1                       isobutylbenzene
  --(1) Ac2O, AlCl3-->  CC(=O)c1ccc(CC(C)C)cc1                 4-isobutylacetophenone
  --(2) ClCH2CO2Et, NaOEt-->  CCOC(=O)C1OC1(C)c1ccc(CC(C)C)cc1  glycidic ester
  --(3) NaOH, then H3O+/Δ-->  CC(C)Cc1ccc(cc1)C(C)C=O           2-(4-isobutylphenyl)propanal
  --(4) NaClO2, NaH2PO4-->   CC(C)Cc1ccc(cc1)C(C)C(=O)O         ibuprofen
```
**Scale basis:** 56.0 mmol (7.52 g) isobutylbenzene → **~5.0 g ibuprofen** (24.3 mmol). If you buy 4-isobutylacetophenone, skip Step 1 and charge 8.39 g (47.6 mmol) at Step 2.

---

## Step 1 — Friedel–Crafts acylation (85 %)

| Reagent | eq | mmol | Mass / vol |
|---|---|---|---|
| Isobutylbenzene | 1.00 | 56.0 | 7.52 g (8.7 mL, d 0.862) |
| AlCl₃ (anhydrous) | 1.30 | 72.8 | 9.71 g |
| Acetic anhydride | 1.20 | 67.2 | 6.86 g (6.3 mL, d 1.082) |
| Dry DCM | — | — | 50 mL |

Suspend AlCl₃ in DCM under N₂, cool to 0 °C, add the arene in DCM (10 mL), then add Ac₂O dropwise keeping <10 °C. Warm to RT, stir 3–4 h (TLC, hexanes/EtOAc 9:1). Quench **slowly onto 100 g ice + 15 mL conc. HCl**, extract DCM (3 × 30 mL), brine, Na₂SO₄, concentrate, short-path vacuum distil (≈140–155 °C/10–15 mmHg).

**Expected:** 8.39 g (47.6 mmol) colourless oil, ~90:10 *p*:*o*. *para*-Isomer shows a clean AA′BB′ pattern; the *o*-isomer gives extra aromatic peaks — remove by distillation.

## Step 2 — Darzens glycidic ester (75 %)

| Reagent | eq | mmol | Mass / vol |
|---|---|---|---|
| 4-Isobutylacetophenone | 1.00 | 47.6 | 8.39 g |
| Na (→ NaOEt in situ) | 1.15 | 54.7 | 1.26 g |
| Ethyl chloroacetate | 1.25 | 59.5 | 7.29 g (6.4 mL, d 1.14) |
| Abs. EtOH | — | — | 40 mL |

Dissolve Na in abs. EtOH (H₂ evolves), add the ketone, stir 15 min, add ethyl chloroacetate dropwise, then reflux 8–12 h. Cool, pour into water/ice, extract MTBE (3 × 40 mL), brine, dry, concentrate. Short-path distil **or use crude** (wash well).

**Expected:** 9.37 g (35.7 mmol) **ethyl 3-methyl-3-(4-isobutylphenyl)oxirane-2-carboxylate** — oil.

## Step 3 — Hydrolysis / decarboxylation → aldehyde (80 %)

| Reagent | eq | Amount |
|---|---|---|
| Glycidic ester | 1.00 | 9.37 g (35.7 mmol) |
| NaOH | 4.0 | 5.71 g in 55 mL H₂O |
| EtOH (co-solvent) | — | 4
… [3843 more bytes]

## Host record of real tool runs

No tool was ever run through the host.

## Final answer

**Note:** the AiZynthFinder retrosynthesis tooling isn't installed in this environment (`scripts/run_retrosynthesis.sh` and the `aizynthfinder` module are both missing), so route selection below is from established literature rather than a fresh model search.

# Best lab route to ibuprofen (racemic)

**Route chosen:** Boots/Darzens route — 4 steps from isobutylbenzene, all bench-scale, no autoclave, ~43 % overall.

| | Industrial "green" route (BHC/Hoechst) | This route (Boots) |
|---|---|---|
| Steps | 3 | 4 |
| Key step | Pd-catalysed CO carbonylation (30–50 bar CO) | Darzens glycidic ester + oxidation |
| Lab at 5 g? | Poor (CO toxicity, pressure reactor) | Straightforward |

**Scheme (SMILES):**
```
CC(C)Cc1ccccc1                       isobutylbenzene
  --(1) Ac2O, AlCl3-->  CC(=O)c1ccc(CC(C)C)cc1                 4-isobutylacetophenone
  --(2) ClCH2CO2Et, NaOEt-->  CCOC(=O)C1OC1(C)c1ccc(CC(C)C)cc1  glycidic ester
  --(3) NaOH, then H3O+/Δ-->  CC(C)Cc1ccc(cc1)C(C)C=O           2-(4-isobutylphenyl)propanal
  --(4) NaClO2, NaH2PO4-->   CC(C)Cc1ccc(cc1)C(C)C(=O)O         ibuprofen
```
**Scale basis:** 56.0 mmol (7.52 g) isobutylbenzene → **~5.0 g ibuprofen** (24.3 mmol). If you buy 4-isobutylacetophenone, skip Step 1 and charge 8.39 g (47.6 mmol) at Step 2.

---

## Step 1 — Friedel–Crafts acylation (85 %)

| Reagent | eq | mmol | Mass / vol |
|---|---|---|---|
| Isobutylbenzene | 1.00 | 56.0 | 7.52 g (8.7 mL, d 0.862) |
| AlCl₃ (anhydrous) | 1.30 | 72.8 | 9.71 g |
| Acetic anhydride | 1.20 | 67.2 | 6.86 g (6.3 mL, d 1.082) |
| Dry DCM | — | — | 50 mL |

Suspend AlCl₃ in DCM under N₂, cool to 0 °C, add the arene in DCM (10 mL), then add Ac₂O dropwise keeping <10 °C. Warm to RT, stir 3–4 h (TLC, hexanes/EtOAc 9:1). Quench **slowly onto 100 g ice + 15 mL conc. HCl**, extract DCM (3 × 30 mL), brine, Na₂SO₄, concentrate, short-path vacuum distil (≈140–155 °C/10–15 mmHg).

**Expected:** 8.39 g (47.6 mmol) colourless oil, ~90:10 *p*:*o*. *para*-Isomer shows a clean AA′BB′ pattern; the *o*-isomer gives extra aromatic peaks — remove by distillation.

## Step 2 — Darzens glycidic ester (75 %)

| Reagent | eq | mmol | Mass / vol |
|---|---|---|---|
| 4-Isobutylacetophenone | 1.00 | 47.6 | 8.39 g |
| Na (→ NaOEt in situ) | 1.15 | 54.7 | 1.26 g |
| Ethyl chloroacetate | 1.25 | 59.5 | 7.29 g (6.4 mL, d 1.14) |
| Abs. EtOH | — | — | 40 mL |

Dissolve Na in abs. EtOH (H₂ evolves), add the ketone, stir 15 min, add ethyl chloroacetate dropwise, then reflux 8–12 h. Cool, pour into water/ice, extract MTBE (3 × 40 mL), brine, dry, concentrate. Short-path distil **or use crude** (wash well).

**Expected:** 9.37 g (35.7 mmol) **ethyl 3-methyl-3-(4-isobutylphenyl)oxirane-2-carboxylate** — oil.

## Step 3 — Hydrolysis / decarboxylation → aldehyde (80 %)

| Reagent | eq | Amount |
|---|---|---|
| Glycidic ester | 1.00 | 9.37 g (35.7 mmol) |
| NaOH | 4.0 | 5.71 g in 55 mL H₂O |
| EtOH (co-solvent) | — | 4
… [3843 more bytes]
