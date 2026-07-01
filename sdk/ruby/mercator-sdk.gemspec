require_relative "lib/mercator/version"

Gem::Specification.new do |spec|
  spec.name = "mercator-sdk"
  spec.version = Mercator::VERSION
  spec.summary = "Ruby SDK for the Mercator V1 HTTP API"
  spec.description = "Dependency-free Ruby client for the Mercator V1 HTTP API."
  spec.authors = ["Mercator Contributors"]
  spec.homepage = "https://github.com/benngarcia/mercator"
  spec.license = "Apache-2.0"
  spec.required_ruby_version = ">= 3.1"

  spec.files = Dir.chdir(__dir__) do
    Dir["lib/**/*.rb", "README.md", "LICENSE"]
  end
  spec.require_paths = ["lib"]

  spec.add_development_dependency "minitest", "~> 5"
  spec.add_development_dependency "webrick", "~> 1.8"
  spec.metadata = {
    "source_code_uri" => spec.homepage
  }
end
