// t2bfm — a thin CLI wrapper around Apple's on-device FoundationModels,
// speaking the same `-p "<prompt>" --output-format json -> {"response": "..."}`
// convention as t2b's other AI backends (claude/gemini/agy), so it drops
// straight into internal/brain.GenericCLI on the Go side. --output-format
// and --model are accepted and ignored: output is always this JSON shape,
// and there is only one on-device model to pick from.
import FoundationModels
import Foundation

struct Envelope: Codable {
    let response: String
}

func fail(_ message: String) -> Never {
    FileHandle.standardError.write((message + "\n").data(using: .utf8)!)
    exit(1)
}

var prompt: String?
var args = CommandLine.arguments.dropFirst().makeIterator()
while let arg = args.next() {
    if arg == "-p" || arg == "--prompt" {
        prompt = args.next()
    }
    // --output-format / --model / anything else: accepted, ignored.
}

guard let prompt, !prompt.isEmpty else {
    fail("usage: t2bfm -p \"<prompt>\" [--output-format json]")
}

let model = SystemLanguageModel.default
switch model.availability {
case .available:
    break
case .unavailable(let reason):
    fail("FoundationModels unavailable: \(reason)")
}

do {
    let session = LanguageModelSession()
    let result = try await session.respond(to: prompt)
    let envelope = Envelope(response: result.content)
    let data = try JSONEncoder().encode(envelope)
    print(String(data: data, encoding: .utf8)!)
} catch {
    fail("t2bfm generation failed: \(error)")
}
