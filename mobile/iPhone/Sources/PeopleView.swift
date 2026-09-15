import SwiftUI

/// The people screen: who you are, who else there is, and who is in your chat.
///
/// Both halves only work once this device's key is in `authorizedUsers` — the
/// username and directory endpoints sit behind the same authorization as
/// everything else — so an unauthorized device is told that instead of being
/// given a form that could only fail.
struct PeopleView: View {
    @EnvironmentObject private var client: ChatClient
    @Environment(\.dismiss) private var dismiss

    @State private var draftName = ""
    @State private var nameNotice: String?
    @State private var nameFailed = false
    @State private var savingName = false

    @State private var query = ""
    @State private var results: [DirectoryEntry] = []
    @State private var searchNotice: String?
    @State private var searchTask: Task<Void, Never>?

    @FocusState private var nameFocused: Bool

    /// How long typing has to pause before the directory is searched. Every
    /// keystroke is a query on the server otherwise.
    private static let searchDebounce: Duration = .milliseconds(250)

    var body: some View {
        NavigationStack {
            Form {
                usernameSection
                searchSection
                chatSection
            }
            .navigationTitle("People")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .onAppear {
            draftName = client.username
            if client.username.isEmpty { nameFocused = true }
            search(query)
        }
        .onDisappear { searchTask?.cancel() }
    }

    // MARK: - Username

    private var canEdit: Bool {
        client.status != .unauthorized
    }

    private var nameChanged: Bool {
        let trimmed = draftName.trimmingCharacters(in: .whitespaces)
        return !trimmed.isEmpty && trimmed != client.username
    }

    @ViewBuilder
    private var usernameSection: some View {
        Section {
            HStack {
                TextField("username", text: $draftName)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .focused($nameFocused)
                    .onSubmit(saveName)
                    .disabled(!canEdit)

                Button(savingName ? "Saving…" : "Save", action: saveName)
                    .buttonStyle(.borderless)
                    .disabled(!canEdit || !nameChanged || savingName)
            }
            if let nameNotice {
                Text(nameNotice)
                    .font(.system(size: 12))
                    .foregroundStyle(nameFailed ? Color.red : .secondary)
            }
        } header: {
            Text("Your username")
        } footer: {
            if canEdit {
                Text("This is the name other people search for to add you. 3–20 characters: letters, digits, and . _ - between them.")
            } else {
                Text("This device has to be authorized before it can claim a username. Settings ▸ This device has the key to authorize.")
            }
        }
    }

    private func saveName() {
        let name = draftName.trimmingCharacters(in: .whitespaces)
        guard canEdit, !name.isEmpty, !savingName else { return }
        savingName = true
        nameNotice = nil
        nameFocused = false

        Task {
            do {
                try await client.setUsername(name)
                draftName = client.username
                nameFailed = false
                nameNotice = "Saved. Other people can find you as \(client.username)."
                // A rename changes what the directory calls this device, and a
                // first name changes who can be added at all.
                search(query)
            } catch {
                nameFailed = true
                nameNotice = error.localizedDescription
            }
            savingName = false
        }
    }

    // MARK: - Search

    @ViewBuilder
    private var searchSection: some View {
        Section {
            HStack {
                Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                TextField("Search usernames", text: $query)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .disabled(!canEdit)
                    .onChange(of: query) { _, value in search(value) }
            }

            if let searchNotice {
                Text(searchNotice)
                    .font(.system(size: 13))
                    .foregroundStyle(.secondary)
            }

            ForEach(results) { entry in
                HStack {
                    Text(entry.username)
                    Spacer()
                    if entry.inChat {
                        Text("In chat")
                            .font(.system(size: 13))
                            .foregroundStyle(.secondary)
                    } else {
                        Button("Add") { add(entry.username) }
                            .buttonStyle(.borderless)
                            .font(.system(size: 14, weight: .semibold))
                    }
                }
            }
        } header: {
            Text("Add people")
        } footer: {
            Text("Adding someone puts you in each other's chat, so they can answer straight away.")
        }
    }

    /// Runs a search after the typing settles. The previous one is cancelled, so
    /// a fast typist causes one query rather than one per letter.
    private func search(_ text: String) {
        searchTask?.cancel()
        guard canEdit else { return }
        searchTask = Task {
            try? await Task.sleep(for: Self.searchDebounce)
            guard !Task.isCancelled else { return }
            do {
                let found = try await client.searchUsers(text)
                guard !Task.isCancelled else { return }
                results = found
                searchNotice = found.isEmpty
                    ? (text.isEmpty ? "Nobody else has chosen a username yet."
                                    : "Nobody by that name.")
                    : nil
            } catch {
                results = []
                searchNotice = error.localizedDescription
            }
        }
    }

    private func add(_ username: String) {
        Task {
            do {
                try await client.addToChat(username)
                search(query)
            } catch {
                searchNotice = error.localizedDescription
            }
        }
    }

    // MARK: - The chat

    @ViewBuilder
    private var chatSection: some View {
        Section {
            if client.chatMembers.isEmpty {
                Text("No one yet — search above to add someone.")
                    .foregroundStyle(.secondary)
            } else {
                ForEach(client.chatMembers, id: \.self) { member in
                    Text(member)
                }
                .onDelete(perform: remove)
            }
        } header: {
            Text("In your chat")
        } footer: {
            Text("Everyone here sees the messages you send. Swipe to remove someone; they leave your chat and you leave theirs.")
        }
    }

    private func remove(at offsets: IndexSet) {
        let leaving = offsets.map { client.chatMembers[$0] }
        Task {
            for member in leaving {
                do {
                    try await client.removeFromChat(member)
                } catch {
                    searchNotice = error.localizedDescription
                }
            }
            search(query)
        }
    }
}
