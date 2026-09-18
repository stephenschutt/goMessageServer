import SwiftUI
import UIKit

/// Where the server is, whether this device has been let in, and the key that
/// gets it let in. Who this device *is* — its username and the people it talks
/// to — belongs to the server rather than to this device, so it is edited on the
/// People screen instead.
struct SettingsView: View {
    @EnvironmentObject private var client: ChatClient
    @Environment(\.dismiss) private var dismiss

    @State private var draftURL = ""
    @State private var copied = false
    @State private var confirmingRegenerate = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    LabeledContent("Username",
                                   value: client.username.isEmpty ? "Not chosen yet" : client.username)
                } header: {
                    Text("You are")
                } footer: {
                    Text("Set it on the People screen — the person icon in the top left of the conversation.")
                }

                Section {
                    TextField("https://messages.schuttsm.com", text: $draftURL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                        .onSubmit(applyURL)
                } header: {
                    Text("Server")
                } footer: {
                    Text("Defaults to the deployed server. To use one running on your Mac instead: the simulator can reach http://localhost:8080, a physical iPhone needs your Mac's address on the same Wi-Fi, e.g. http://192.168.1.20:8080.")
                }

                Section {
                    LabeledContent("Status", value: statusText)
                }

                identitySection
            }
            .confirmationDialog("Create a new key?", isPresented: $confirmingRegenerate, titleVisibility: .visible) {
                Button("Create new key", role: .destructive) {
                    Task { await client.regenerateKey() }
                    copied = false
                }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("The current key is discarded. The new one has to be authorized on the server before this device can send messages again.")
            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") {
                        applyURL()
                        dismiss()
                    }
                }
            }
        }
        .onAppear { draftURL = client.serverURL }
    }

    private func applyURL() {
        let trimmed = draftURL.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, trimmed != client.serverURL else { return }
        client.serverURL = trimmed
    }

    private var statusText: String {
        switch client.status {
        case .connecting: return "Connecting…"
        case .connected: return "Connected"
        case .unauthorized: return "Waiting to be authorized"
        case .failed(let reason): return reason
        }
    }

    /// This device's public key. The server only lets a key through once it is
    /// in the authorizedUsers table, so the key has to be readable and copyable
    /// here — it is what the person running the server pastes into
    /// `messageServer -authorize`. The private half stays in the device's
    /// local database and is deliberately not shown or sent anywhere.
    @ViewBuilder
    private var identitySection: some View {
        Section {
            if let key = client.publicKey {
                Text(key)
                    .font(.system(size: 11, design: .monospaced))
                    .lineLimit(4)
                    .truncationMode(.middle)
                    .textSelection(.enabled)

                Button {
                    UIPasteboard.general.string = key
                    copied = true
                } label: {
                    Label(copied ? "Copied" : "Copy public key",
                          systemImage: copied ? "checkmark" : "doc.on.doc")
                }

                Button("Create a new key", role: .destructive) {
                    confirmingRegenerate = true
                }
            } else {
                Text("Creating this device\u{2019}s key…")
                    .foregroundStyle(.secondary)
            }
        } header: {
            Text("This device")
        } footer: {
            Text("Only the public key ever leaves this device. Authorize it on the server with:\n\nmessageServer -authorize \u{2018}<key>\u{2019}")
        }
    }
}
